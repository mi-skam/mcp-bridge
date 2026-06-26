package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleSetupAddGrepGlobal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ZOT_HOME", tmp)

	out, err := handleSetup([]string{"add", "grep"}, tmp)
	if err != nil {
		t.Fatalf("handleSetup add grep: %v", err)
	}
	if out == "" {
		t.Fatal("expected setup output")
	}

	data, err := os.ReadFile(filepath.Join(tmp, "mcp.json"))
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse mcp.json: %v", err)
	}
	grep := cfg.MCPServers["grep"]
	if grep.Transport != "streamable-http" || grep.URL != "https://mcp.grep.app/" {
		t.Fatalf("unexpected grep config: %+v", grep)
	}
	if len(grep.Headers) != 0 {
		t.Fatalf("protocol headers must not be written to mcp.json: %+v", grep.Headers)
	}
}

func TestHandleSetupAddFilesystemProject(t *testing.T) {
	cwd := t.TempDir()

	_, err := handleSetup([]string{"add", "filesystem", "--project"}, cwd)
	if err != nil {
		t.Fatalf("handleSetup add filesystem: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, ".zot", "mcp.json"))
	if err != nil {
		t.Fatalf("read project mcp.json: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse project mcp.json: %v", err)
	}
	fs := cfg.MCPServers["filesystem"]
	if fs.Command != "npx" || len(fs.Args) != 3 || fs.Args[2] != cwd {
		t.Fatalf("unexpected filesystem config: %+v", fs)
	}
}

func TestHandleSetupDuplicate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ZOT_HOME", tmp)

	if _, err := handleSetup([]string{"add", "grep"}, tmp); err != nil {
		t.Fatalf("first add failed: %v", err)
	}
	if _, err := handleSetup([]string{"add", "grep"}, tmp); err == nil {
		t.Fatal("expected duplicate server error")
	}
}
