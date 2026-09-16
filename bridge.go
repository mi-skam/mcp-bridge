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
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
)

const (
	mcpSearchToolName      = "mcp__search_tools"
	defaultToolSearchLimit = 8
	maxToolSearchLimit     = 20
)

var mcpSearchToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Words describing the MCP capability needed, such as 'search GitHub code' or 'list n8n workflows'."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 20,
      "description": "Maximum number of matching tools to load. Defaults to 8."
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`)

// toolMapping tracks which zot tool name maps to which MCP server + tool.
type toolMapping struct {
	serverName  string // e.g. "filesystem"
	mcpTool     string // e.g. "read_file"
	description string
}

// bridge connects MCP servers to zot's extension protocol.
type bridge struct {
	e   *ext.Extension
	cwd string
	// servers is written once in loadServers before any goroutine
	// starts, and read-only afterwards — that invariant is what makes
	// the lock-free reads in handleToolCall and the idle reaper safe.
	// Do not add or remove entries after startup.
	servers map[string]*managedServer
	mapping map[string]toolMapping // zot tool name → MCP server + tool; guarded by mu
	logger  *log.Logger

	mu               sync.Mutex // guards mapping and searchRegistered
	searchRegistered bool
}

// newBridge creates a new MCP→zot bridge.
func newBridge(e *ext.Extension, cwd string, logger *log.Logger) *bridge {
	return &bridge{
		e:       e,
		cwd:     cwd,
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
		srv := newManagedServer(name, srvCfg, b.cwd, b.logger)
		b.servers[name] = srv
	}
}

// registerToolSearch exposes the only eager MCP tool definition. The actual
// MCP tools stay deferred until this local catalog search activates a small,
// relevant subset. This keeps large MCP installations from bloating every LLM
// request and is required by providers with tight tool-schema size limits.
func (b *bridge) registerToolSearch() {
	b.mu.Lock()
	if b.searchRegistered {
		b.mu.Unlock()
		return
	}
	b.searchRegistered = true
	b.mu.Unlock()

	b.e.Tool(mcpSearchToolName,
		"Search configured MCP tools by capability and load the matching tool definitions. Use this before calling an MCP tool that is not already available.",
		mcpSearchToolSchema,
		b.searchTools,
	)
}

// registerCachedTools registers previously discovered tool definitions without
// starting MCP servers. Tool calls still lazy-start the owning server on demand.
func (b *bridge) registerCachedTools(cache toolCache) int {
	b.registerToolSearch()
	count := 0
	for serverName, srv := range b.servers {
		cached, ok := cache.Servers[serverName]
		if !ok || cached.Fingerprint != serverFingerprint(srv.config) {
			continue
		}
		cachedNames := make([]mcp.Tool, 0, len(cached.Tools))
		for _, tool := range cached.Tools {
			b.registerCachedTool(serverName, tool)
			cachedNames = append(cachedNames, mcp.Tool{Name: tool.Name})
			count++
		}
		srv.mu.Lock()
		if len(srv.tools) == 0 {
			srv.tools = cachedNames
		}
		srv.mu.Unlock()
	}
	return count
}

// refreshToolCache discovers live tools and updates the on-disk cache.
// It reports whether the cache changed; callers should only ask for /reload-ext
// when changed is true.
func (b *bridge) refreshToolCache(ctx context.Context, path string) (bool, error) {
	previous, err := readToolCache(path)
	if err != nil {
		b.logger.Printf("read existing tool cache: %v", err)
	}
	cache := toolCache{Version: toolCacheVersion, Servers: map[string]cachedServer{}}
	var wg sync.WaitGroup
	errCh := make(chan error, len(b.servers))
	var mu sync.Mutex

	for name, srv := range b.servers {
		wg.Add(1)
		go func(n string, s *managedServer) {
			defer wg.Done()
			if err := s.start(ctx); err != nil {
				b.logger.Printf("[%s] lazy discovery failed: %v", n, err)
				errCh <- fmt.Errorf("%s: %w", n, err)
				return
			}

			s.mu.Lock()
			tools := append([]mcp.Tool(nil), s.tools...)
			s.mu.Unlock()

			cachedTools := make([]cachedTool, 0, len(tools))
			for _, tool := range tools {
				ct, err := cachedToolFromMCP(n, tool)
				if err != nil {
					b.logger.Printf("[%s] tool %s: schema marshal error: %v", n, tool.Name, err)
					continue
				}
				cachedTools = append(cachedTools, ct)
			}

			sort.Slice(cachedTools, func(i, j int) bool { return cachedTools[i].Name < cachedTools[j].Name })

			mu.Lock()
			cache.Servers[n] = cachedServer{Fingerprint: serverFingerprint(s.config), Tools: cachedTools}
			mu.Unlock()
		}(name, srv)
	}

	wg.Wait()
	close(errCh)

	changed := !toolCachesEqual(previous, cache)
	if changed {
		if err := writeToolCache(path, cache); err != nil {
			return false, err
		}
	}

	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return changed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return changed, nil
}

func (b *bridge) registerCachedTool(serverName string, tool cachedTool) {
	zotName := toolName(serverName, tool.Name)

	b.mu.Lock()
	if existing, exists := b.mapping[zotName]; exists {
		b.mu.Unlock()
		if existing.serverName != serverName || existing.mcpTool != tool.Name {
			b.logger.Printf("tool name collision: %s already maps to %s/%s; skipping %s/%s",
				zotName, existing.serverName, existing.mcpTool, serverName, tool.Name)
		}
		return
	}
	b.mapping[zotName] = toolMapping{
		serverName:  serverName,
		mcpTool:     tool.Name,
		description: tool.Description,
	}
	b.mu.Unlock()

	b.e.DeferredTool(zotName, tool.Description, json.RawMessage(tool.Schema), func(args json.RawMessage) ext.ToolResult {
		return b.handleToolCall(zotName, args)
	})

	b.logger.Printf("registered deferred cached tool: %s → %s/%s", zotName, serverName, tool.Name)
}

type toolSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type toolSearchMatch struct {
	name        string
	description string
	score       int
}

func (b *bridge) searchTools(raw json.RawMessage) ext.ToolResult {
	var args toolSearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return ext.TextErrorResult(mcpSearchToolName + ": invalid arguments: " + err.Error())
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return ext.TextResult("No MCP tools loaded: provide a non-empty capability query.")
	}
	limit := args.Limit
	if limit == 0 {
		limit = defaultToolSearchLimit
	}
	if limit < 1 || limit > maxToolSearchLimit {
		return ext.TextErrorResult(fmt.Sprintf("%s: limit must be between 1 and %d", mcpSearchToolName, maxToolSearchLimit))
	}

	matches := b.findTools(query, limit)
	if len(matches) == 0 {
		return ext.TextResult(fmt.Sprintf("No MCP tools matched %q. Try broader capability words or an MCP server name.", query))
	}

	var text strings.Builder
	fmt.Fprintf(&text, "Loaded %d MCP tool(s) matching %q:\n", len(matches), query)
	activated := make([]string, 0, len(matches))
	for _, match := range matches {
		activated = append(activated, match.name)
		fmt.Fprintf(&text, "- %s", match.name)
		if match.description != "" {
			fmt.Fprintf(&text, ": %s", match.description)
		}
		text.WriteByte('\n')
	}
	text.WriteString("Call the appropriate loaded tool now. Search again if none fits the task.")
	return ext.ToolResult{
		Content:       []ext.ToolContent{ext.Text(text.String())},
		ActivateTools: activated,
	}
}

func (b *bridge) findTools(query string, limit int) []toolSearchMatch {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 || limit <= 0 {
		return nil
	}

	b.mu.Lock()
	matches := make([]toolSearchMatch, 0, len(b.mapping))
	for name, mapping := range b.mapping {
		score := scoreToolMatch(terms, name, mapping)
		if score > 0 {
			matches = append(matches, toolSearchMatch{name: name, description: mapping.description, score: score})
		}
	}
	b.mu.Unlock()

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func scoreToolMatch(terms []string, zotName string, mapping toolMapping) int {
	name := strings.ToLower(zotName)
	server := strings.ToLower(mapping.serverName)
	mcpName := strings.ToLower(mapping.mcpTool)
	description := strings.ToLower(mapping.description)
	score := 0
	matched := 0
	for _, term := range terms {
		termScore := 0
		switch {
		case term == mcpName || term == name:
			termScore = 100
		case strings.Contains(mcpName, term):
			termScore = 60
		case strings.Contains(server, term):
			termScore = 40
		case strings.Contains(name, term):
			termScore = 30
		case strings.Contains(description, term):
			termScore = 15
		}
		if termScore > 0 {
			matched++
			score += termScore
		}
	}
	if matched == len(terms) {
		score += 25
	}
	return score
}

func mcpToolDescription(serverName string, tool mcp.Tool) string {
	desc := tool.Description
	if tool.Annotations.Title != "" {
		desc = tool.Annotations.Title + ": " + desc
	}

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
	return desc
}

// mcpToolSchema converts an MCP Tool's input schema to a JSON Schema value.
func mcpToolSchema(tool mcp.Tool) any {
	if len(tool.RawInputSchema) > 0 {
		return json.RawMessage(tool.RawInputSchema)
	}

	schema := map[string]any{
		"type":       tool.InputSchema.Type,
		"properties": tool.InputSchema.Properties,
	}
	if len(tool.InputSchema.Required) > 0 {
		schema["required"] = tool.InputSchema.Required
	}
	if len(tool.InputSchema.Defs) > 0 {
		schema["$defs"] = tool.InputSchema.Defs
	}
	if tool.InputSchema.AdditionalProperties != nil {
		schema["additionalProperties"] = tool.InputSchema.AdditionalProperties
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

	// managedServer owns all timeouts (connect + request); pass a
	// plain context here.
	result, err := srv.callTool(context.Background(), mapping.mcpTool, args)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ext.TextErrorResult(timeoutErrorMessage(mapping.serverName, srv.config.RequestTimeout))
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection") || strings.Contains(errMsg, "transport") {
			return ext.TextErrorResult(fmt.Sprintf(
				"Connection to MCP server '%s' failed: %v. The server may have crashed or been stopped. "+
					"Try running '/mcp start %s' to restart that server, or '/mcp restart' to restart all servers.",
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

// timeoutErrorMessage formats the user-facing text for a deadline error.
func timeoutErrorMessage(serverName string, requestTimeout int) string {
	return fmt.Sprintf(
		"Tool call timed out after %d seconds. The MCP server '%s' may be slow or unresponsive. "+
			"You can increase the timeout in your mcp.json config with 'requestTimeout'.",
		requestTimeout, serverName)
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

// startAll starts all configured servers without re-registering their tools with zot.
func (b *bridge) startAll(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(b.servers))

	for name, srv := range b.servers {
		wg.Add(1)
		go func(n string, s *managedServer) {
			defer wg.Done()
			if err := s.start(ctx); err != nil {
				b.logger.Printf("[%s] failed to start: %v", n, err)
				errCh <- fmt.Errorf("%s: %w", n, err)
			}
		}(name, srv)
	}

	wg.Wait()
	close(errCh)

	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// startIdleReaper runs a background goroutine that kills idle servers.
// It has no shutdown mechanism by design: it lives exactly as long as
// the extension process, and zot reaps the whole process on exit.
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

// serverStatus returns compact status info for all servers in stable order.
func (b *bridge) serverStatus() []string {
	names := make([]string, 0, len(b.servers))
	for name := range b.servers {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, b.servers[name].status())
	}
	return lines
}

func (b *bridge) notifyLevel() string {
	if len(b.servers) == 0 {
		return "info"
	}
	ready := 0
	errored := 0
	for _, srv := range b.servers {
		srv.mu.Lock()
		state := srv.state
		srv.mu.Unlock()
		switch state {
		case stateReady:
			ready++
		case stateError:
			errored++
		}
	}
	if errored > 0 && ready == 0 {
		return "error"
	}
	if errored > 0 {
		return "warn"
	}
	return "success"
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
