package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInstallConfigRejectsMalformedFileWithoutChangingIt(t *testing.T) {
	for _, content := range []string{
		`{`, `null`, `[]`, `"string"`,
		`{"mcpServers": []}`, `{"mcpServers": "not an object"}`,
	} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := installServerConfig(path, "new", ServerConfig{Command: "runner"}); err == nil {
				t.Fatal("expected malformed configuration error")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatalf("file was changed: %q, %v", got, err)
			}
		})
	}
}

func TestInstallConfigConcurrentCommandsPreserveAllEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	const count = 12
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := installServerConfig(path, fmt.Sprintf("server-%d", i), ServerConfig{Command: "runner"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cfg, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != count {
		t.Fatalf("got %d entries, want %d", len(cfg.MCPServers), count)
	}
}
