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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
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
	logger *log.Logger

	mu        sync.Mutex
	state     serverState
	client    *client.Client
	tools     []mcp.Tool
	lastUsed  time.Time
	startErr  error
	stopCh    chan struct{} // closed when server should shut down
}

// newManagedServer creates a new server wrapper.
func newManagedServer(name string, cfg ServerConfig, logger *log.Logger) *managedServer {
	return &managedServer{
		name:    name,
		config:  cfg,
		logger:  logger,
		state:   stateStopped,
		lastUsed: time.Now(),
	}
}

// start spawns the MCP server process and discovers its tools.
// It's safe to call multiple times — subsequent calls are no-ops if already ready.
func (s *managedServer) start(ctx context.Context) error {
	s.mu.Lock()
	if s.state == stateReady {
		s.lastUsed = time.Now()
		s.mu.Unlock()
		return nil
	}
	if s.state == stateStarting {
		s.mu.Unlock()
		// Wait for the other goroutine to finish starting
		return s.waitForReady(ctx)
	}
	s.state = stateStarting
	s.startErr = nil
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	err := s.doStart(ctx)

	s.mu.Lock()
	if err != nil {
		s.state = stateError
		s.startErr = err
		s.logger.Printf("[%s] start failed: %v", s.name, err)
	} else {
		s.state = stateReady
		s.lastUsed = time.Now()
		s.logger.Printf("[%s] ready with %d tools", s.name, len(s.tools))
	}
	s.mu.Unlock()
	return err
}

// doStart performs the actual spawn + initialize + tools/list.
func (s *managedServer) doStart(ctx context.Context) error {
	var c *client.Client
	var err error

	switch s.config.Transport {
	case "stdio", "":
		c, err = s.startStdio()
	case "streamable-http":
		c, err = s.startStreamableHTTP()
	case "sse":
		c, err = s.startSSE()
	default:
		return fmt.Errorf("unknown transport: %s", s.config.Transport)
	}

	if err != nil {
		return err
	}

	// Initialize the MCP session
	initCtx, cancel := context.WithTimeout(ctx, time.Duration(s.config.ConnectTimeout)*time.Second)
	defer cancel()

	_, err = c.Initialize(initCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "zot-mcp-bridge",
				Version: "1.0.0",
			},
			Capabilities: mcp.ClientCapabilities{},
		},
	})
	if err != nil {
		c.Close()
		return fmt.Errorf("initialize: %w", err)
	}

	// Discover tools
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := c.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return fmt.Errorf("list tools: %w", err)
	}

	s.mu.Lock()
	s.client = c
	s.tools = result.Tools
	s.mu.Unlock()

	return nil
}

// startStdio spawns a stdio-based MCP server.
func (s *managedServer) startStdio() (*client.Client, error) {
	if s.config.Command == "" {
		return nil, fmt.Errorf("stdio transport requires 'command' field")
	}

	// Build environment: inherit current env + extra vars
	env := make([]string, 0)
	for k, v := range s.config.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Create stdio client (this spawns the subprocess)
	c, err := client.NewStdioMCPClient(s.config.Command, env, s.config.Args...)
	if err != nil {
		return nil, fmt.Errorf("spawn %s: %w", s.config.Command, err)
	}

	// Capture stderr for debugging
	if stderr, ok := client.GetStderr(c); ok {
		go s.pipeStderr(stderr)
	}

	return c, nil
}

// startStreamableHTTP connects to an HTTP-based MCP server.
func (s *managedServer) startStreamableHTTP() (*client.Client, error) {
	if s.config.URL == "" {
		return nil, fmt.Errorf("streamable-http transport requires 'url' field")
	}

	// Build HTTP options
	opts := []transport.StreamableHTTPCOption{}

	// Streamable HTTP requires Accept: application/json, text/event-stream.
	// This is a protocol detail, so set it automatically. User-provided
	// headers are merged on top for auth/customization.
	headers := map[string]string{
		"Accept": "application/json, text/event-stream",
	}
	for k, v := range s.config.Headers {
		headers[k] = v
	}
	opts = append(opts, transport.WithHTTPHeaders(headers))

	// Create streamable HTTP client
	c, err := client.NewStreamableHttpClient(s.config.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// Start the transport
	if err := c.GetTransport().Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start HTTP transport: %w", err)
	}

	s.logger.Printf("[%s] connected to %s", s.name, s.config.URL)
	return c, nil
}

// startSSE connects to an SSE-based MCP server (legacy transport).
func (s *managedServer) startSSE() (*client.Client, error) {
	if s.config.URL == "" {
		return nil, fmt.Errorf("sse transport requires 'url' field")
	}

	// Create SSE client
	c, err := client.NewSSEMCPClient(s.config.URL)
	if err != nil {
		return nil, fmt.Errorf("create SSE client: %w", err)
	}

	// Start the transport
	if err := c.GetTransport().Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start SSE transport: %w", err)
	}

	s.logger.Printf("[%s] connected to SSE server at %s", s.name, s.config.URL)
	return c, nil
}

// pipeStderr reads server stderr and logs it.
func (s *managedServer) pipeStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			lines := strings.Split(strings.TrimSpace(string(buf[:n])), "\n")
			for _, line := range lines {
				if line != "" {
					s.logger.Printf("[%s:stderr] %s", s.name, line)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// stop gracefully shuts down the server.
func (s *managedServer) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.state != stateError {
		s.state = stateStopped
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
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

	result, err := c.CallTool(callCtx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: argsMap,
		},
	})
	if err != nil {
		return nil, err
	}

	// Update last-used time
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()

	return result, nil
}

// waitForReady blocks until the server is ready or context expires.
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

// status returns a human-readable status string.
func (s *managedServer) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("%s: %s (%d tools)", s.name, s.state, len(s.tools))
}
