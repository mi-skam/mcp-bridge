package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParityConfigAndContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeJSON(t, path, `{"mcpServers":{"http":{"url":"https://example.test/mcp","connectTimeoutMs":15,"requestTimeoutMs":1250},"stdio":{"type":"stdio","command":"x","url":"https://ignored.test"}}}`)
	cfg := Config{MCPServers: map[string]ServerConfig{}}
	if err := mergeConfig(&cfg, path); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCPServers["http"]
	if s.Transport != "streamable-http" || s.connectDuration() != 15*time.Millisecond || s.requestDuration() != 1250*time.Millisecond {
		t.Fatalf("config: %+v", s)
	}
	if cfg.MCPServers["stdio"].Transport != "stdio" {
		t.Fatal("explicit transport lost")
	}
	writeJSON(t, path, `{"mcpServers":{"bad":{"requestTimeoutMs":-1}}}`)
	if mergeConfig(&cfg, path) == nil {
		t.Fatal("negative timeout accepted")
	}
	if got := projectRootURI("/tmp/my project/#notes"); got != "file:///tmp/my%20project/%23notes" {
		t.Fatal(got)
	}
	r := mcpResultToZot(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "summary"}}, StructuredContent: map[string]any{"value": 42}})
	if len(r.Content) != 2 || !strings.Contains(text(r), `"value":42`) {
		t.Fatal("structured output lost")
	}
	r = resourceContentsToZot([]*mcp.ResourceContents{{URI: "mem://blob", MIMEType: "application/octet-stream", Blob: []byte{0, 1, 2}}})
	var content mcp.ResourceContents
	if err := json.Unmarshal([]byte(text(r)), &content); err != nil {
		t.Fatal(err)
	}
	if string(content.Blob) != string([]byte{0, 1, 2}) {
		t.Fatal("binary content lost")
	}
}
