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
	"os"
	"os/exec"
	"strings"
	"sync"
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

	mu       sync.Mutex
	state    serverState
	client   *mcp.ClientSession
	tools    []*mcp.Tool
	lastUsed time.Time
	startErr error
	recent []string // bounded lifecycle log; excludes raw server output and credentials
	gen      uint64 // bumped by stop(); a start attempt only commits if unchanged
	loginActive bool // one interactive OAuth flow at a time
	loggedOut   bool // credentials removed via /mcp logout; cleared by a successful start
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
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.config.ConnectTimeout)*time.Second)
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
		if compactErr(err.Error()) == "auth failed" {
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

	client := mcp.NewClient(&mcp.Implementation{Name: "zot-mcp-bridge", Version: version}, nil)
	if s.cwd != "" {
		client.AddRoots(&mcp.Root{URI: "file://" + s.cwd, Name: "project"})
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
	hc := headerHTTPClient(s.config.Headers, time.Duration(s.config.RequestTimeout)*time.Second)
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
// Used for SSE only; streamable-HTTP gets full 401→Authorize handling from go-sdk.
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
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.state != stateError {
		s.state = stateStopped
	}
}

// callTool forwards a tool call to the MCP server.
// If the server is not running, it starts it first.
func (s *managedServer) callTool(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	c := s.client
	st := s.state
	s.mu.Unlock()

	// If not ready, start the server
	if st != stateReady || c == nil {
		if err := s.start(ctx); err != nil {
			return nil, err
		}
		s.mu.Lock()
		c = s.client
		s.mu.Unlock()
		if c == nil {
			return nil, fmt.Errorf("[%s] stopped before the call could run", s.name)
		}
	}

	// Parse args into map[string]any
	var argsMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return nil, fmt.Errorf("invalid args: %w", err)
		}
	}

	// Call the tool
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(s.config.RequestTimeout)*time.Second)
	defer cancel()

	result, err := c.CallTool(callCtx, &mcp.CallToolParams{Name: toolName, Arguments: argsMap})
	if err != nil {
		return nil, err
	}

	// Update last-used time
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()

	return result, nil
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
	if s.state != stateReady {
		return false
	}
	return time.Since(s.lastUsed) > timeout
}

// status returns a compact human-readable status string.
func (s *managedServer) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.state {
	case stateReady:
		return fmt.Sprintf("%s: READY — %s discovered", s.name, toolCountText(len(s.tools)))
	case stateError:
		if s.startErr != nil {
			if compactErr(s.startErr.Error()) == "auth failed" {
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
	if len(s.recent) > 8 { s.recent = s.recent[len(s.recent)-8:] }
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
	sb.WriteString(state.String())
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
	if len(recent) == 0 { sb.WriteString("  No events recorded yet.\n") }
	for _, line := range recent { sb.WriteString("  "+line+"\n") }
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
