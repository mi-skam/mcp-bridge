package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicWritesContentAndPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "file.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("content = %q, want %q", data, "{}\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}

func TestWriteFileAtomicLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	if err := writeFileAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "file.json" {
		t.Fatalf("directory not clean: %v", entries)
	}
}

func TestToolCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-tools-cache.json")
	in := toolCache{
		Version: toolCacheVersion,
		Servers: map[string]cachedServer{
			"srv": {Fingerprint: "abc", Tools: []cachedTool{
				{Name: "t1", Description: "d", Schema: []byte(`{"type":"object"}`)},
			}},
		},
	}
	if err := writeToolCache(path, in); err != nil {
		t.Fatalf("writeToolCache: %v", err)
	}
	out, err := readToolCache(path)
	if err != nil {
		t.Fatalf("readToolCache: %v", err)
	}
	if !toolCachesEqual(in, out) {
		t.Fatalf("round trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestToolCacheVersionMismatchInvalidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-tools-cache.json")
	if err := os.WriteFile(path, []byte(`{"version": 999, "servers": {"srv": {}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := readToolCache(path)
	if err != nil {
		t.Fatalf("readToolCache: %v", err)
	}
	if len(out.Servers) != 0 || out.Version != toolCacheVersion {
		t.Fatalf("stale-version cache must be discarded, got %+v", out)
	}
}

func TestConfigFileWrittenOwnerOnly(t *testing.T) {
	for i, scope := range []string{"local", "project", "user"} {
		t.Run(scope, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			args := []string{"--scope", scope, "--transport", "http", "remote", "https://example.com/mcp", "--header", "Authorization: Bearer secret"}
			if _, err := handleInstall(args, cwd); err != nil {
				t.Fatalf("handleInstall: %v", err)
			}
			path := installTestPaths(home, cwd)[i]
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat mcp.json: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("mcp.json perm = %o, want 600 (may contain auth headers)", got)
			}
		})
	}
}
