package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("ZOT_HOME", home)

	writeJSON(t, filepath.Join(home, "mcp.json"), `{
		"mcpServers": {
			"shared": {"command": "global-cmd"},
			"global-only": {"command": "g"}
		}
	}`)
	writeJSON(t, filepath.Join(proj, ".zot", "mcp.json"), `{
		"mcpServers": {
			"shared": {"command": "project-cmd"}
		}
	}`)

	cfg, err := loadConfig(proj)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.MCPServers["shared"].Command; got != "project-cmd" {
		t.Fatalf("project must override global: shared.command = %q", got)
	}
	if _, ok := cfg.MCPServers["global-only"]; !ok {
		t.Fatal("global-only server lost in merge")
	}
	// Defaults applied during merge:
	if got := cfg.MCPServers["shared"].RequestTimeout; got != 60 {
		t.Fatalf("default requestTimeout = %d, want 60", got)
	}
}

func TestLoadConfigMissingFilesIsNotAnError(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	cfg, err := loadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("loadConfig with no files: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("expected empty config, got %+v", cfg.MCPServers)
	}
}

func TestHandleSetupProjectRequiresCwd(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	if _, err := handleSetup([]string{"add", "grep", "--project"}, ""); err == nil {
		t.Fatal("expected error for --project with unknown working directory")
	}
}
