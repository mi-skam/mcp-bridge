package main

import (
	"path/filepath"
	"testing"
)

// zot-mcp (TypeScript) configuration compatibility.
func TestLoadConfigZotMCPCompat(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ZOT_HOME", filepath.Join(home, "zot"))
	t.Setenv("MCP_COMPAT_TOKEN", "tok")

	writeJSON(t, filepath.Join(home, ".config", "mcp", "mcp.json"), `{"mcpServers":{
		"shared":{"command":"config-cmd"},
		"gone":{"command":"x","disabled":true},
		"ms":{"command":"x","connectTimeoutMs":1500,"requestTimeoutMs":60000}
	}}`)
	writeJSON(t, filepath.Join(home, ".agents", "mcp.json"), `{"mcpServers":{"shared":{"command":"agents-cmd"}}}`)
	writeJSON(t, filepath.Join(home, ".agents", "mcp", "mcp.json"), `{"mcpServers":{"shared":{"command":"agents-dir-cmd"}}}`)
	writeJSON(t, filepath.Join(home, "zot", "mcp.json"), `{"mcpServers":{
		"shared":{"command":"zot-cmd","cwd":"~/work","headers":{"Authorization":"Bearer $env:MCP_COMPAT_TOKEN"}},
		"rel":{"command":"x","cwd":"sub/dir"}
	}}`)

	cfg, err := loadConfig(proj)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	shared := cfg.MCPServers["shared"]
	if shared.Command != "zot-cmd" {
		t.Fatalf("precedence: shared.command = %q, want zot-cmd", shared.Command)
	}
	if shared.Cwd != filepath.Join(home, "work") {
		t.Fatalf("~ expansion: cwd = %q", shared.Cwd)
	}
	if shared.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("$env: expansion: %q", shared.Headers["Authorization"])
	}
	if got := cfg.MCPServers["rel"].Cwd; got != filepath.Join(proj, "sub", "dir") {
		t.Fatalf("relative cwd = %q", got)
	}
	if s, ok := cfg.MCPServers["gone"]; !ok || !s.Disabled {
		t.Fatal("disabled server must remain configured")
	}
	ms := cfg.MCPServers["ms"]
	if ms.ConnectTimeout != 30 || ms.RequestTimeout != 60 || ms.ConnectTimeoutMs != 1500 || ms.RequestTimeoutMs != 60000 {
		t.Fatalf("ms aliases: %+v", ms)
	}
}
