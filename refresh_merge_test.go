package main

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"
)

// A session whose cwd has no project servers must not wipe the cache entries
// another cwd relies on, and a server that fails discovery keeps its entry.
func TestRefreshToolCacheKeepsUnseenAndFailedServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-tools-cache.json")
	previous := toolCache{
		Version: toolCacheVersion,
		Servers: map[string]cachedServer{
			"relaxdays-atlassian": {Fingerprint: "fp-a", Tools: []cachedTool{{Name: "jira_search", Schema: []byte(`{}`)}}},
			"obsidian":            {Fingerprint: "fp-o", Tools: []cachedTool{{Name: "note", Schema: []byte(`{}`)}}},
		},
	}
	if err := writeToolCache(path, previous); err != nil {
		t.Fatal(err)
	}

	// Only "obsidian" is configured here, pointing at a port nobody listens on.
	b := newBridge(nil, t.TempDir(), log.New(io.Discard, "", 0))
	b.loadServers(Config{MCPServers: map[string]ServerConfig{
		"obsidian": {Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", ConnectTimeout: 1},
	}})

	changed, err := b.refreshToolCache(context.Background(), path)
	if err == nil {
		t.Fatal("expected discovery error for unreachable server")
	}
	if changed {
		t.Fatal("nothing was discovered, cache must be unchanged")
	}

	out, err := readToolCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !toolCachesEqual(previous, out) {
		t.Fatalf("cache was rewritten:\nwant %+v\ngot  %+v", previous, out)
	}
}
