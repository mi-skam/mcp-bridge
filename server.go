// server.go — MCP server process lifecycle management.
//
// Each configured MCP server is wrapped in a managedServer that tracks:
//   - Connection state (stopped / starting / ready)
//   - Last-access time (for idle timeout)
//   - Discovered tools
//
// Smart lazy: servers are spawned on startup to discover tools, then
// killed after an idle period. On the next tool call, they're respawned.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverState tracks the lifecycle of one MCP server.
type serverState int

const (
	stateStopped  serverState = iota // not running
	stateStarting                    // spawning / initializing
	stateReady                       // connected, tools discovered
	stateError                       // failed to start
)

func (s serverState) String() string {
	switch s {
	case stateStopped:
		return "stopped"
	case stateStarting:
		return "starting"
	case stateReady:
		return "ready"
	case stateError:
		return "error"
	default:
		return "unknown"
	}
}

// managedServer wraps one MCP server with lifecycle management.
type managedServer struct {
	name   string
	config ServerConfig
	cwd    string
	logger *log.Logger

	mu          sync.Mutex
	state       serverState
	client      *mcp.ClientSession
	tools       []*mcp.Tool
	lastUsed    time.Time
	startErr    error
	recent      []string // bounded lifecycle log; excludes raw server output and credentials
	gen         uint64   // bumped by stop(); a start attempt only commits if unchanged
	loginActive bool     // one interactive OAuth flow at a time
	loggedOut   bool     // credentials removed via /mcp logout; cleared by a successful start

	// events receives server-initiated notifications for the user. nil = drop.
	events func(level, message string)
	// onToolsChanged runs when the server announces tools/list_changed.
	onToolsChanged func(server string)
	subscriptions  map[string]bool // active session only; prevents idle shutdown
	requestID      atomic.Uint64
}

// markLoggedOut records an explicit credential removal so status can say so
// before any start attempt fails.
func (s *managedServer) markLoggedOut() {
	s.mu.Lock()
	s.loggedOut = true
	s.recordEvent("LOGOUT: local credentials removed")
	s.mu.Unlock()
}

// beginLogin claims the interactive OAuth slot; false when a flow is already running.
func (s *managedServer) beginLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loginActive {
		return false
	}
	s.loginActive = true
	s.recordEvent("AUTH started")
	return true
}

func (s *managedServer) endLogin() {
	s.mu.Lock()
	s.loginActive = false
	s.mu.Unlock()
}

// newManagedServer creates a new server wrapper.
func newManagedServer(name string, cfg ServerConfig, cwd string, logger *log.Logger) *managedServer {
	return &managedServer{
		name:     name,
		config:   cfg,
		cwd:      cwd,
		logger:   logger,
		state:    stateStopped,
		lastUsed: time.Now(),
	}
}

// start spawns the MCP server process and discovers its tools.
// Safe to call concurrently; a stop() issued while a start is in
// flight wins — the late result is discarded and its client closed.
func (s *managedServer) start(ctx context.Context) error {
	if s.config.Disabled {
		return fmt.Errorf("server %q is disabled", s.name)
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.connectDuration())
	defer cancel()

	s.mu.Lock()
	if s.state == stateReady {
		s.lastUsed = time.Now()
		s.mu.Unlock()
		return nil
	}
	if s.state == stateStarting {
		s.mu.Unlock()
		return s.waitForReady(ctx)
	}
	s.state = stateStarting
	s.recordEvent("CONNECTING")
	s.startErr = nil
	gen := s.gen
	s.mu.Unlock()

	c, tools, err := s.doStart(ctx)
	return s.finishStart(gen, c, tools, err)
}

// finishStart commits the outcome of a start attempt, unless stop()
// bumped the generation in the meantime.
func (s *managedServer) finishStart(gen uint64, c *mcp.ClientSession, tools []*mcp.Tool, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gen != gen {
		if c != nil {
			c.Close()
		}
		return fmt.Errorf("[%s] stopped while starting", s.name)
	}
	if err != nil {
		s.state = stateError
		s.startErr = err
		s.recordEvent("FAILED (see current error)")
		s.logger.Printf("[%s] start failed: %v", s.name, err)
		if needsAuth(err) {
			// Stored token expired and refresh was rejected (client registration purged, grant revoked)
			// or the token endpoint was unreachable. mcp-go collapses both into one error.
			s.recordEvent("AUTH REQUIRED: stored token unusable and refresh failed")
		}
		return err
	}
	s.client = c
	s.tools = tools
	s.state = stateReady
	s.loggedOut = false
	s.recordEvent(fmt.Sprintf("READY: %d tools discovered", len(tools)))
	s.lastUsed = time.Now()
	s.logger.Printf("[%s] ready with %d tools", s.name, len(s.tools))
	return nil
}

// doStart connects (spawn or HTTP), initializes, and lists tools.
func (s *managedServer) doStart(ctx context.Context) (*mcp.ClientSession, []*mcp.Tool, error) {
	return s.connect(ctx, nil)
}

// connect performs one connection attempt. sess carries an explicit
// interactive OAuth flow (/mcp auth); nil means background: stored
// credentials may be used and refreshed, but no browser ever opens.
func (s *managedServer) connect(ctx context.Context, sess *oauthSession) (*mcp.ClientSession, []*mcp.Tool, error) {
	var t mcp.Transport
	var err error
	switch s.config.Transport {
	case "stdio", "":
		t, err = s.stdioTransport()
	case "streamable-http", "sse":
		t, err = s.httpTransport(sess)
	default:
		return nil, nil, fmt.Errorf("unknown transport: %s", s.config.Transport)
	}
	if err != nil {
		return nil, nil, err
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-bridge", Version: version}, s.clientOptions())
	if s.cwd != "" {
		client.AddRoots(&mcp.Root{URI: projectRootURI(s.cwd), Name: "project"})
	}
	cs, err := client.Connect(ctx, t, nil)
	if err != nil {
		if errors.Is(err, errAuthRequired) {
			return nil, nil, fmt.Errorf("initialize: %w", errAuthRequired)
		}
		return nil, nil, fmt.Errorf("initialize: %w", err)
	}
	if s.config.Transport != "stdio" && s.config.Transport != "" {
		s.logger.Printf("[%s] connected to %s", s.name, s.config.URL)
	}

	var tools []*mcp.Tool
	var cursor string
	for {
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			cs.Close()
			return nil, nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return cs, tools, nil
}

func projectRootURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// clientOptions wires server-initiated notifications. Logging at warning and
// above reaches the user; everything else goes to the extension log.
func (s *managedServer) clientOptions() *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			s.recordEventLocked("TOOLS CHANGED (server notification)")
			if s.onToolsChanged != nil {
				go s.onToolsChanged(s.name)
			}
		},
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			if s.events != nil {
				p := r.Params
				s.events("info", fmt.Sprintf("%s [%v]: %g/%g %s", s.name, p.ProgressToken, p.Progress, p.Total, p.Message))
			}
		},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
			s.catalogChanged("resources")
		},
		PromptListChangedHandler: func(context.Context, *mcp.PromptListChangedRequest) {
			s.catalogChanged("prompts")
		},
		LoggingMessageHandler: func(_ context.Context, r *mcp.LoggingMessageRequest) {
			msg := fmt.Sprint(r.Params.Data)
			if b, err := json.Marshal(r.Params.Data); err == nil {
				msg = string(b)
			}
			s.logger.Printf("[%s:%s] %s", s.name, r.Params.Level, msg)
			if s.events == nil {
				return
			}
			switch r.Params.Level {
			case "warning":
				s.events("warn", fmt.Sprintf("%s: %s", s.name, firstLine(msg)))
			case "error", "critical", "alert", "emergency":
				s.events("error", fmt.Sprintf("%s: %s", s.name, firstLine(msg)))
			}
		},
		ResourceUpdatedHandler: func(_ context.Context, r *mcp.ResourceUpdatedNotificationRequest) {
			s.recordEventLocked("RESOURCE UPDATED " + r.Params.URI)
			if s.events != nil {
				s.events("info", fmt.Sprintf("%s: resource updated %s", s.name, r.Params.URI))
			}
		},
	}
}

// Resource and prompt lists are fetched live, so notification requires no cache invalidation.
func (s *managedServer) catalogChanged(kind string) {
	s.recordEventLocked(strings.ToUpper(kind) + " CHANGED (server notification)")
	if s.events != nil {
		s.events("info", s.name+": "+kind+" list changed")
	}
}

func (s *managedServer) recordEventLocked(message string) {
	s.mu.Lock()
	s.recordEvent(message)
	s.mu.Unlock()
}

// stdioTransport spawns the server process. It runs from the zot session's
// project directory (or the configured cwd), not the extension install dir:
// many servers treat their process cwd as project root. The exec.Cmd is not
// bound to the connect context; the session owns the process lifetime.
func (s *managedServer) stdioTransport() (mcp.Transport, error) {
	if s.config.Command == "" {
		return nil, fmt.Errorf("stdio transport requires 'command' field")
	}
	cmd := exec.Command(s.config.Command, s.config.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.config.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if s.config.Cwd != "" {
		cmd.Dir = s.config.Cwd
	} else if s.cwd != "" {
		cmd.Dir = s.cwd
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	go s.pipeStderr(stderr)
	return &mcp.CommandTransport{Command: cmd}, nil
}

// httpTransport builds a streamable-HTTP or SSE transport with static headers
// and, when credentials exist or a login is running, go-sdk OAuth.
func (s *managedServer) httpTransport(sess *oauthSession) (mcp.Transport, error) {
	if s.config.URL == "" {
		return nil, fmt.Errorf("%s transport requires 'url' field", s.config.Transport)
	}
	hc := headerHTTPClient(s.config.Headers, s.config.requestDuration())
	if sess == nil {
		var err error
		if sess, err = s.newOAuthSession(false, nil); err != nil {
			return nil, err
		}
	}
	var handler auth.OAuthHandler
	if sess != nil {
		var err error
		if handler, err = sess.handler(); err != nil {
			return nil, err
		}
	}
	if s.config.Transport == "sse" {
		if handler != nil {
			// ponytail: go-sdk SSEClientTransport has no OAuthHandler field; wrap the client instead.
			hc = oauthRoundTripper(hc, handler)
		}
		return &mcp.SSEClientTransport{Endpoint: s.config.URL, HTTPClient: hc}, nil
	}
	return &mcp.StreamableClientTransport{Endpoint: s.config.URL, HTTPClient: hc, OAuthHandler: handler}, nil
}

// headerHTTPClient adds static headers (auth tokens etc.) to every request.
func headerHTTPClient(headers map[string]string, timeout time.Duration) *http.Client {
	hc := &http.Client{Timeout: timeout}
	if len(headers) > 0 {
		hc.Transport = headerRoundTripper{headers, http.DefaultTransport}
	}
	return hc
}

type headerRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.next.RoundTrip(r)
}

// oauthRoundTripper adds a bearer token from the handler's token source.
// Used for SSE only; retry an authorization challenge once, never loop.
func oauthRoundTripper(hc *http.Client, h auth.OAuthHandler) *http.Client {
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	out := *hc
	out.Transport = bearerRoundTripper{h, next}
	return &out
}

type bearerRoundTripper struct {
	h    auth.OAuthHandler
	next http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := b.send(r)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	// A consumed body cannot be retried unless the caller provided GetBody.
	if r.Body != nil && r.Body != http.NoBody && r.GetBody == nil {
		return resp, nil
	}
	if err := b.h.Authorize(r.Context(), r, resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()
	retry := r.Clone(r.Context())
	if r.GetBody != nil {
		retry.Body, err = r.GetBody()
		if err != nil {
			return nil, err
		}
	}
	return b.send(retry)
}

func (b bearerRoundTripper) send(r *http.Request) (*http.Response, error) {
	ts, err := b.h.TokenSource(r.Context())
	if err != nil {
		return nil, err
	}
	if ts != nil {
		tok, err := ts.Token()
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		tok.SetAuthHeader(r)
	}
	return b.next.RoundTrip(r)
}

// pipeStderr reads server stderr line by line and logs it.
func (s *managedServer) pipeStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			s.logger.Printf("[%s:stderr] %s", s.name, line)
		}
	}
}

// stop gracefully shuts down the server.
func (s *managedServer) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recordEvent("STOP requested")
	s.gen++ // invalidate any start attempt still in flight
	s.subscriptions = nil
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.state != stateError {
		s.state = stateStopped
	}
}

// withSession runs fn against a ready session, starting the server first if
// needed, under the configured request timeout. It is the single entry point
// for every MCP request the bridge forwards.
func (s *managedServer) withSession(ctx context.Context, fn func(context.Context, *mcp.ClientSession) error) error {
	if s.config.Disabled {
		return fmt.Errorf("server %q is disabled", s.name)
	}
	s.mu.Lock()
	c := s.client
	st := s.state
	s.mu.Unlock()

	if st != stateReady || c == nil {
		if err := s.start(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		c = s.client
		s.mu.Unlock()
		if c == nil {
			return fmt.Errorf("[%s] stopped before the call could run", s.name)
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, s.config.requestDuration())
	defer cancel()
	if err := fn(callCtx, c); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
	return nil
}

// callTool forwards a tool call to the MCP server.
func (s *managedServer) callTool(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	var argsMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return nil, fmt.Errorf("invalid args: %w", err)
		}
	}
	var result *mcp.CallToolResult
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) (err error) {
		params := &mcp.CallToolParams{Name: toolName, Arguments: argsMap}
		params.SetProgressToken(fmt.Sprintf("%s:%d", toolName, s.requestID.Add(1)))
		result, err = c.CallTool(ctx, params)
		return err
	})
	return result, err
}

// liveTools returns the current tool list from a live session (not the cache).
func (s *managedServer) liveTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) error {
		var cursor string
		for {
			res, err := c.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return err
			}
			tools = append(tools, res.Tools...)
			if res.NextCursor == "" {
				return nil
			}
			cursor = res.NextCursor
		}
	})
	if err == nil {
		s.mu.Lock()
		s.tools = tools
		s.mu.Unlock()
	}
	return tools, err
}

// listResources returns concrete resources and templates. Servers without the
// resources capability yield empty lists, not an error.
func (s *managedServer) listResources(ctx context.Context) ([]*mcp.Resource, []*mcp.ResourceTemplate, error) {
	var res []*mcp.Resource
	var tpl []*mcp.ResourceTemplate
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) error {
		if caps := c.InitializeResult().Capabilities; caps == nil || caps.Resources == nil {
			return nil
		}
		for cursor := ""; ; {
			r, err := c.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
			if err != nil {
				return err
			}
			res = append(res, r.Resources...)
			if cursor = r.NextCursor; cursor == "" {
				break
			}
		}
		for cursor := ""; ; {
			r, err := c.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
			if err != nil {
				return err
			}
			tpl = append(tpl, r.ResourceTemplates...)
			if cursor = r.NextCursor; cursor == "" {
				break
			}
		}
		return nil
	})
	return res, tpl, err
}

func (s *managedServer) resourceSubscription(ctx context.Context, uri string, subscribe bool) error {
	return s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) error {
		var err error
		if subscribe {
			err = c.Subscribe(ctx, &mcp.SubscribeParams{URI: uri})
		} else {
			err = c.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri})
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.client != c {
			return fmt.Errorf("connection stopped during subscription change")
		}
		if subscribe {
			if s.subscriptions == nil {
				s.subscriptions = make(map[string]bool)
			}
			s.subscriptions[uri] = true
		} else {
			delete(s.subscriptions, uri)
		}
		return nil
	})
}

func (s *managedServer) readResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	var out *mcp.ReadResourceResult
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) (err error) {
		out, err = c.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		return err
	})
	return out, err
}

// listPrompts returns the prompt catalogue; empty for servers without the capability.
func (s *managedServer) listPrompts(ctx context.Context) ([]*mcp.Prompt, error) {
	var out []*mcp.Prompt
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) error {
		if caps := c.InitializeResult().Capabilities; caps == nil || caps.Prompts == nil {
			return nil
		}
		for cursor := ""; ; {
			r, err := c.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
			if err != nil {
				return err
			}
			out = append(out, r.Prompts...)
			if cursor = r.NextCursor; cursor == "" {
				return nil
			}
		}
	})
	return out, err
}

func (s *managedServer) getPrompt(ctx context.Context, name string, args map[string]string) (*mcp.GetPromptResult, error) {
	var out *mcp.GetPromptResult
	err := s.withSession(ctx, func(ctx context.Context, c *mcp.ClientSession) (err error) {
		out, err = c.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
		return err
	})
	return out, err
}

// waitForReady blocks until the server is ready or the context
// expires. Polling is a deliberate simplification for this example;
// a channel closed by finishStart would avoid the 100ms latency.
func (s *managedServer) waitForReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.mu.Lock()
			st := s.state
			err := s.startErr
			s.mu.Unlock()
			if st == stateReady {
				return nil
			}
			if st == stateError {
				return err
			}
		}
	}
}

// isIdle returns true if the server hasn't been used within the timeout.
func (s *managedServer) isIdle(timeout time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateReady || len(s.subscriptions) > 0 {
		return false
	}
	return time.Since(s.lastUsed) > timeout
}

// status returns a compact human-readable status string.
func (s *managedServer) status() string {
	if s.config.Disabled {
		return s.name + ": DISABLED"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.state {
	case stateReady:
		return fmt.Sprintf("%s: READY — %s discovered", s.name, toolCountText(len(s.tools)))
	case stateError:
		if s.startErr != nil {
			if needsAuth(s.startErr) {
				return fmt.Sprintf("%s: LOGIN REQUIRED — /mcp auth %s", s.name, s.name)
			}
			return fmt.Sprintf("%s: FAILED — %s", s.name, compactErr(s.startErr.Error()))
		}
		return fmt.Sprintf("%s: FAILED", s.name)
	case stateStopped:
		if s.loggedOut {
			return fmt.Sprintf("%s: NOT AUTHORIZED — /mcp auth %s", s.name, s.name)
		}
		if len(s.tools) > 0 {
			return fmt.Sprintf("%s: SLEEPING — %s cached, not a live check", s.name, toolCountText(len(s.tools)))
		}
		return fmt.Sprintf("%s: NOT CONNECTED — /mcp start %s", s.name, s.name)
	case stateStarting:
		return fmt.Sprintf("%s: CONNECTING", s.name)
	default:
		return fmt.Sprintf("%s (%s)", s.name, s.state)
	}
}

// recordEvent requires s.mu to be held. Only bridge-generated safe messages go here.
func (s *managedServer) recordEvent(message string) {
	s.recent = append(s.recent, time.Now().Format("15:04:05")+" "+message)
	if len(s.recent) > 8 {
		s.recent = s.recent[len(s.recent)-8:]
	}
}

// detailStatus returns a multi-line status for one server.
func (s *managedServer) detailStatus(registered int) string {
	s.mu.Lock()
	name := s.name
	transport := s.config.Transport
	url := s.config.URL
	command := s.config.Command
	args := append([]string(nil), s.config.Args...)
	state := s.state
	toolCount := len(s.tools)
	idleTimeout := s.config.IdleTimeout
	requestTimeout := s.config.RequestTimeout
	connectTimeout := s.config.ConnectTimeout
	lastUsed := s.lastUsed
	startErr := s.startErr
	recent := append([]string(nil), s.recent...)
	s.mu.Unlock()

	if transport == "" {
		transport = "stdio"
	}

	var sb strings.Builder
	sb.WriteString("MCP server: ")
	sb.WriteString(name)
	sb.WriteByte('\n')
	sb.WriteString("  status: ")
	if s.config.Disabled {
		sb.WriteString("disabled")
	} else {
		sb.WriteString(state.String())
	}
	if state == stateError && startErr != nil {
		sb.WriteString(" (")
		sb.WriteString(compactErr(startErr.Error()))
		sb.WriteByte(')')
	}
	sb.WriteByte('\n')
	if state == stateReady {
		sb.WriteString(fmt.Sprintf("  tools discovered (last successful connection): %d\n", toolCount))
	} else {
		sb.WriteString(fmt.Sprintf("  tools known (cached/previous discovery; not live): %d\n", toolCount))
	}
	sb.WriteString(fmt.Sprintf("  tools registered in this extension session (deferred): %d\n", registered))
	// ponytail: counts cannot detect same-size schema changes; the refresh notification covers those.
	if toolCount != registered {
		sb.WriteString("  Tool counts differ. After a successful /mcp refresh, run /reload-ext.\n")
	}
	sb.WriteString("  transport: ")
	sb.WriteString(transport)
	sb.WriteByte('\n')
	if url != "" {
		sb.WriteString("  url: ")
		sb.WriteString(url)
		sb.WriteByte('\n')
	}
	if command != "" {
		sb.WriteString("  command: ")
		sb.WriteString(command)
		if len(args) > 0 {
			sb.WriteByte(' ')
			sb.WriteString(strings.Join(args, " "))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(fmt.Sprintf("  timeouts: connect=%ds request=%ds idle=%ds\n", connectTimeout, requestTimeout, idleTimeout))
	if !lastUsed.IsZero() {
		sb.WriteString("  last used: ")
		sb.WriteString(time.Since(lastUsed).Round(time.Second).String())
		sb.WriteString(" ago\n")
	}
	if state == stateError && startErr != nil {
		sb.WriteString("  error: ")
		sb.WriteString(firstLine(startErr.Error()))
		sb.WriteByte('\n')
	}
	sb.WriteString("\nRECENT LIFECYCLE LOG (this extension session)\n")
	if len(recent) == 0 {
		sb.WriteString("  No events recorded yet.\n")
	}
	for _, line := range recent {
		sb.WriteString("  " + line + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func compactToolStatus(name string, n int) string {
	if n <= 0 {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, toolCountText(n))
}

func toolCountText(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return fmt.Sprintf("%d tools", n)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	return s
}

// needsAuth reports whether a start error means the user must run /mcp auth.
// errAuthRequired is the typed signal from oauth.go; the string match covers a
// plain 401/403 from a server with no stored credentials at all.
func needsAuth(err error) bool {
	return err != nil && (errors.Is(err, errAuthRequired) || compactErr(err.Error()) == "auth failed")
}

func compactErr(s string) string {
	s = firstLine(s)
	if s == "" {
		return "error"
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "authorization") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "401") || strings.Contains(lower, "403"):
		return "auth failed"
	case strings.Contains(lower, "transport") || strings.Contains(lower, "connection") || strings.Contains(lower, "connect"):
		return "connection failed"
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "timeout"
	case strings.Contains(lower, "no such file") || strings.Contains(lower, "executable file not found"):
		return "command not found"
	case strings.HasPrefix(lower, "initialize:"):
		return "initialize failed"
	case strings.HasPrefix(lower, "list tools:"):
		return "tool discovery failed"
	}
	const max = 44
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
