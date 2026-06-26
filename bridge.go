// bridge.go — MCP tool → zot tool registration and routing.
//
// Converts MCP tools into zot-registered tools with namespaced names:
//
//	mcp__<server>__<tool>
//
// The double underscore separates the server name from the tool name,
// avoiding collisions with zot's built-in tools (read, write, edit, bash, skill).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
)

// toolMapping tracks which zot tool name maps to which MCP server + tool.
type toolMapping struct {
	serverName string // e.g. "filesystem"
	mcpTool    string // e.g. "read_file"
}

// bridge connects MCP servers to zot's extension protocol.
type bridge struct {
	e       *ext.Extension
	servers map[string]*managedServer
	mapping map[string]toolMapping // zot tool name → MCP server + tool
	logger  *log.Logger

	mu sync.Mutex
}

// newBridge creates a new MCP→zot bridge.
func newBridge(e *ext.Extension, logger *log.Logger) *bridge {
	return &bridge{
		e:       e,
		servers: make(map[string]*managedServer),
		mapping: make(map[string]toolMapping),
		logger:  logger,
	}
}

// sanitizeName converts a string into a valid zot tool name component.
// Zot tool names must match [a-zA-Z][a-zA-Z0-9_]*.
var invalidChars = regexp.MustCompile(`[^a-zA-Z0-9]`)

func sanitizeName(s string) string {
	s = invalidChars.ReplaceAllString(s, "_")
	// Ensure it starts with a letter
	if len(s) > 0 && (s[0] >= '0' && s[0] <= '9') {
		s = "t_" + s
	}
	if s == "" {
		s = "unnamed"
	}
	return s
}

// toolName builds the namespaced zot tool name for an MCP tool.
func toolName(serverName, mcpToolName string) string {
	return fmt.Sprintf("mcp__%s__%s", sanitizeName(serverName), sanitizeName(mcpToolName))
}

// loadServers reads the config and creates managed servers.
func (b *bridge) loadServers(cfg Config) {
	for name, srvCfg := range cfg.MCPServers {
		srv := newManagedServer(name, srvCfg, b.logger)
		b.servers[name] = srv
	}
}

// discoverAndRegister starts all servers, discovers their tools,
// and registers them with zot.
func (b *bridge) discoverAndRegister(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(b.servers))

	// Start all servers concurrently
	for name, srv := range b.servers {
		wg.Add(1)
		go func(n string, s *managedServer) {
			defer wg.Done()
			if err := s.start(ctx); err != nil {
				b.logger.Printf("[%s] failed to start: %v", n, err)
				errCh <- fmt.Errorf("%s: %w", n, err)
				return
			}
			// Register each tool with zot
			s.mu.Lock()
			tools := s.tools
			s.mu.Unlock()

			for _, tool := range tools {
				b.registerTool(n, tool)
			}
		}(name, srv)
	}

	wg.Wait()
	close(errCh)

	// Collect errors but don't fail — partial success is fine
	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		b.logger.Printf("some servers failed: %s", strings.Join(errs, "; "))
	}

	return nil
}

// registerTool registers one MCP tool with zot.
func (b *bridge) registerTool(serverName string, tool mcp.Tool) {
	zotName := toolName(serverName, tool.Name)

	// Build JSON schema for zot
	schema := mcpToolSchema(tool)
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		b.logger.Printf("[%s] tool %s: schema marshal error: %v", serverName, tool.Name, err)
		return
	}

	// Build description with annotations
	desc := tool.Description
	if tool.Annotations.Title != "" {
		desc = tool.Annotations.Title + ": " + desc
	}
	
	// Add annotation hints to description
	var hints []string
	if tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint {
		hints = append(hints, "read-only")
	}
	if tool.Annotations.IdempotentHint != nil && *tool.Annotations.IdempotentHint {
		hints = append(hints, "idempotent")
	}
	if tool.Annotations.OpenWorldHint != nil && !*tool.Annotations.OpenWorldHint {
		hints = append(hints, "closed-world")
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		hints = append(hints, "destructive")
	}
	if len(hints) > 0 {
		desc += " [" + strings.Join(hints, ", ") + "]"
	}
	
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from server %q", serverName)
	}

	// Record the mapping
	b.mu.Lock()
	b.mapping[zotName] = toolMapping{
		serverName: serverName,
		mcpTool:    tool.Name,
	}
	b.mu.Unlock()

	// Register with zot
	b.e.Tool(zotName, desc, json.RawMessage(schemaJSON), func(args json.RawMessage) ext.ToolResult {
		return b.handleToolCall(zotName, args)
	})

	b.logger.Printf("registered tool: %s → %s/%s", zotName, serverName, tool.Name)
}

// mcpToolSchema converts an MCP Tool's InputSchema to a JSON Schema map.
func mcpToolSchema(tool mcp.Tool) map[string]any {
	schema := map[string]any{
		"type":       tool.InputSchema.Type,
		"properties": tool.InputSchema.Properties,
	}
	if len(tool.InputSchema.Required) > 0 {
		schema["required"] = tool.InputSchema.Required
	}
	return schema
}

// handleToolCall routes a zot tool call to the appropriate MCP server.
func (b *bridge) handleToolCall(zotName string, args json.RawMessage) ext.ToolResult {
	b.mu.Lock()
	mapping, ok := b.mapping[zotName]
	b.mu.Unlock()

	if !ok {
		return ext.TextErrorResult(fmt.Sprintf(
			"Tool '%s' not found. This tool was registered but is no longer available. "+
				"The MCP server may have been stopped. Try running '/mcp' to check server status.",
			zotName))
	}

	srv, ok := b.servers[mapping.serverName]
	if !ok {
		return ext.TextErrorResult(fmt.Sprintf(
			"MCP server '%s' not found. The server configuration may have been removed. "+
				"Check your mcp.json configuration file.",
			mapping.serverName))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(srv.config.RequestTimeout)*time.Second)
	defer cancel()

	result, err := srv.callTool(ctx, mapping.mcpTool, args)
	if err != nil {
		// Provide more helpful error messages
		errMsg := err.Error()
		if strings.Contains(errMsg, "timeout") {
			return ext.TextErrorResult(fmt.Sprintf(
				"Tool call timed out after %d seconds. The MCP server '%s' may be slow or unresponsive. "+
					"You can increase the timeout in your mcp.json config with 'requestTimeout'.",
				srv.config.RequestTimeout, mapping.serverName))
		}
		if strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "transport") {
			return ext.TextErrorResult(fmt.Sprintf(
				"Connection to MCP server '%s' failed: %v. The server may have crashed or been stopped. "+
					"Try running '/mcp:restart %s' to restart the server.",
				mapping.serverName, err, mapping.serverName))
		}
		return ext.TextErrorResult(fmt.Sprintf(
			"MCP tool call failed: %v. Server: %s, Tool: %s. "+
				"Check '/mcp %s' for server status.",
			err, mapping.serverName, mapping.mcpTool, mapping.serverName))
	}

	// Convert MCP result to zot ToolResult
	return mcpResultToZot(result)
}

// mcpResultToZot converts an MCP CallToolResult to a zot ToolResult.
func mcpResultToZot(result *mcp.CallToolResult) ext.ToolResult {
	if result == nil {
		return ext.TextResult("(no result)")
	}

	var contents []ext.ToolContent
	for _, c := range result.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			contents = append(contents, ext.Text(v.Text))
		case mcp.ImageContent:
			contents = append(contents, ext.Image(v.MIMEType, v.Data))
		default:
			// Fallback: try to marshal as JSON
			data, _ := json.Marshal(v)
			contents = append(contents, ext.Text(string(data)))
		}
	}

	if len(contents) == 0 {
		contents = append(contents, ext.Text("(empty result)"))
	}

	tr := ext.ToolResult{Content: contents}
	if result.IsError {
		tr.IsError = true
	}
	return tr
}

// startIdleReaper runs a background goroutine that kills idle servers.
func (b *bridge) startIdleReaper() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			for _, srv := range b.servers {
				// Use per-server idle timeout
				idleTimeout := time.Duration(srv.config.IdleTimeout) * time.Second
				if srv.isIdle(idleTimeout) {
					b.logger.Printf("[%s] idle timeout, stopping", srv.name)
					srv.stop()
				}
			}
		}
	}()
}

// stopAll shuts down all MCP servers.
func (b *bridge) stopAll() {
	for _, srv := range b.servers {
		srv.stop()
	}
}

// serverStatus returns status info for all servers.
func (b *bridge) serverStatus() []string {
	var lines []string
	for _, srv := range b.servers {
		lines = append(lines, srv.status())
	}
	return lines
}

// startServer manually starts a specific server.
func (b *bridge) startServer(name string) error {
	srv, ok := b.servers[name]
	if !ok {
		return fmt.Errorf("unknown server: %s", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.start(ctx)
}

// stopServer manually stops a specific server.
func (b *bridge) stopServer(name string) error {
	srv, ok := b.servers[name]
	if !ok {
		return fmt.Errorf("unknown server: %s", name)
	}
	srv.stop()
	return nil
}
