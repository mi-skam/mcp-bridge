package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestHandleInstallLocalAndProjectRequireCwd(t *testing.T) {
	for _, options := range [][]string{nil, {"--scope", "local"}, {"--scope", "project"}, {"--project"}} {
		t.Run(strings.Join(options, " "), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ZOT_HOME", home)
			args := append(append([]string{}, options...), "server", "runner")
			if _, err := handleInstall(args, ""); err == nil {
				t.Fatalf("handleInstall(%q): expected error with unknown working directory", args)
			}
			if _, err := os.Stat(filepath.Join(home, "mcp.json")); !os.IsNotExist(err) {
				t.Fatalf("missing cwd must not fall back to user scope: %v", err)
			}
		})
	}
}

func TestHandleInstallUserDoesNotRequireCwd(t *testing.T) {
	for _, options := range [][]string{{"--scope", "user"}, {"--global"}} {
		t.Run(strings.Join(options, " "), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ZOT_HOME", home)
			args := append(append([]string{}, options...), "server", "runner")
			if _, err := handleInstall(args, ""); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfigFile(filepath.Join(home, "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MCPServers["server"].Command != "runner" {
				t.Fatalf("user install missing: %+v", cfg.MCPServers)
			}
		})
	}
}

func TestHandleInstallMalformedConfigUnchanged(t *testing.T) {
	for _, original := range []string{`{"mcpServers":`, `{"mcpServers":[]}`, `{"mcpServers":"invalid"}`, `[]`} {
		t.Run(original, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("ZOT_HOME", t.TempDir())
			path := filepath.Join(cwd, ".zot", "mcp.json")
			writeJSON(t, path, original)
			if _, err := handleInstall([]string{"server", "runner"}, cwd); err == nil {
				t.Fatal("expected malformed config error")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != original {
				t.Fatalf("malformed config was overwritten: %s", data)
			}
		})
	}
}
