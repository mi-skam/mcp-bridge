package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigEnvironmentExpansion(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	t.Setenv("MCP_TEST_VALUE", "synthetic\"\\token")
	t.Setenv("MCP_TEST_EMPTY", "")
	t.Setenv("MCP_TEST_LITERAL", "${MCP_TEST_VALUE}")
	const absent = "MCP_TEST_UNSET"
	old, set := os.LookupEnv(absent)
	os.Unsetenv(absent)
	t.Cleanup(func() {
		if set {
			os.Setenv(absent, old)
		} else {
			os.Unsetenv(absent)
		}
	})
	project := t.TempDir()
	writeJSON(t, filepath.Join(os.Getenv("ZOT_HOME"), "mcp.json"), `{"mcpServers":{"override":{"command":"${MCP_TEST_UNSET}"}}}`)
	writeJSON(t, filepath.Join(project, ".zot", "mcp.json"), `{"mcpServers":{
 "override":{"command":"ok"},
 "valid":{"command":"${MCP_TEST_VALUE}","args":["${MCP_TEST_UNSET:-fallback}","${MCP_TEST_EMPTY:-fallback}","$HOME","${MCP_TEST_LITERAL}","${MCP_TEST_UNSET:-}"],"env":{"TOKEN":"${MCP_TEST_VALUE}"},"url":"${MCP_TEST_UNSET:-https://example.test}/mcp","headers":{"Authorization":"Bearer ${MCP_TEST_VALUE}"}},
 "invalid":{"headers":{"Authorization":"secret-prefix ${MCP_TEST_UNSET}"}}
 }}`)
	cfg, err := loadConfig(project)
	if err == nil || !strings.Contains(err.Error(), absent) || !strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "secret-prefix") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
	if _, ok := cfg.MCPServers["invalid"]; ok {
		t.Fatal("invalid server must not be started")
	}
	if cfg.MCPServers["override"].Command != "ok" {
		t.Fatal("expand after merging")
	}
	s := cfg.MCPServers["valid"]
	if s.Command != os.Getenv("MCP_TEST_VALUE") || s.Env["TOKEN"] != s.Command || s.Headers["Authorization"] != "Bearer "+s.Command {
		t.Fatal("command/env/header expansion failed")
	}
	if s.URL != "https://example.test/mcp" || strings.Join(s.Args, "|") != "fallback||$HOME|${MCP_TEST_VALUE}|" {
		t.Fatalf("unexpected URL/args: %s %v", s.URL, s.Args)
	}
}
