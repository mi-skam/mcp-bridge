package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Keep every possible configuration/state location inside the test sandbox,
// including lower-precedence user configurations that uninstall must not search.
func uninstallTestSandbox(t *testing.T) (root, cwd string, paths map[string]string) {
	t.Helper()
	root = t.TempDir()
	cwd = filepath.Join(root, "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Even an implementation that accidentally resolves an empty cwd against
	// the process working directory must stay inside this sandbox.
	t.Chdir(cwd)
	for key, dir := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "APPDATA": "appdata",
		"XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "ZOT_HOME": "zot",
	} {
		t.Setenv(key, filepath.Join(root, dir))
	}
	return root, cwd, map[string]string{
		"local":   filepath.Join(cwd, ".zot", "mcp.json"),
		"project": filepath.Join(cwd, ".mcp.json"),
		"user":    filepath.Join(zotHome(), "mcp.json"),
	}
}

func uninstallTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type uninstallTestFile struct {
	Mode fs.FileMode
	Data string
}

// Include directories so rejected commands cannot silently create .zot or a
// state directory even when they do not write a configuration file.
func uninstallTestSnapshot(t *testing.T, root string) map[string]uninstallTestFile {
	t.Helper()
	files := map[string]uninstallTestFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := uninstallTestFile{Mode: info.Mode()}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file.Data = string(data)
		}
		files[path] = file
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestUninstallHelp(t *testing.T) {
	for _, want := range []string{"/mcp uninstall", "<name>", "--scope", "-s", "local", "project", "user", "--global", "--project", "Show this help"} {
		if !strings.Contains(uninstallHelp(), want) {
			t.Errorf("uninstall help missing %q:\n%s", want, uninstallHelp())
		}
	}
	for _, args := range [][]string{
		nil, {}, {"help"}, {"-h"}, {"--help"},
		{"target", "--help"}, {"--help", "target"}, {"target", "-h"},
	} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			root, cwd, _ := uninstallTestSandbox(t)
			before := uninstallTestSnapshot(t, root)
			got, err := handleUninstall(args, cwd)
			if err != nil || got != uninstallHelp() {
				t.Fatalf("handleUninstall(%q) = %q, %v; want help", args, got, err)
			}
			if after := uninstallTestSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("help modified or created files/directories")
			}
		})
	}
}

func TestUninstallSelectsOnlyRequestedScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		scope string
	}{
		{"default", []string{"target"}, "local"},
		{"short local before", []string{"-s", "local", "target"}, "local"},
		{"short project after", []string{"target", "-s", "project"}, "project"},
		{"short user before", []string{"-s", "user", "target"}, "user"},
		{"long local after", []string{"target", "--scope", "local"}, "local"},
		{"long project before", []string{"--scope", "project", "target"}, "project"},
		{"long user after", []string{"target", "--scope", "user"}, "user"},
		{"inline local before", []string{"--scope=local", "target"}, "local"},
		{"inline project after", []string{"target", "--scope=project"}, "project"},
		{"inline user before", []string{"--scope=user", "target"}, "user"},
		{"global before", []string{"--global", "target"}, "user"},
		{"global after", []string{"target", "--global"}, "user"},
		{"project before", []string{"--project", "target"}, "project"},
		{"project after", []string{"target", "--project"}, "project"},
		{"separator after scope", []string{"--scope", "project", "--", "target"}, "project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, cwd, paths := uninstallTestSandbox(t)
			for scope, path := range paths {
				for _, name := range []string{"target", "keeper"} {
					if err := installServerConfig(path, name, ServerConfig{Command: scope + "-runner"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := uninstallTestSnapshot(t, root)
			message, err := handleUninstall(tc.args, cwd)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message, "/reload-ext") {
				t.Errorf("success must instruct /reload-ext: %q", message)
			}
			lower := strings.ToLower(message)
			if !strings.Contains(lower, "scope") || !strings.Contains(lower, "activ") {
				t.Errorf("success must warn that same-name entries in other scopes may become active: %q", message)
			}
			cfg, err := readConfigFile(paths[tc.scope])
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.MCPServers) != 1 || cfg.MCPServers["keeper"].Command != tc.scope+"-runner" {
				t.Fatalf("wrong remaining configuration: %+v", cfg)
			}
			after := uninstallTestSnapshot(t, root)
			delete(before, paths[tc.scope])
			delete(after, paths[tc.scope])
			if !reflect.DeepEqual(before, after) {
				t.Fatal("uninstall changed files outside the selected configuration")
			}
		})
	}
}

func TestUninstallEndOfOptionsTreatsFlagsAsNames(t *testing.T) {
	for _, name := range []string{"target", "--global", "--project", "--scope=user", "--help", "-h", "--unknown", "--", "-", "help", "日本語-server"} {
		t.Run(name, func(t *testing.T) {
			_, cwd, paths := uninstallTestSandbox(t)
			if err := installServerConfig(paths["local"], name, ServerConfig{Command: "runner"}); err != nil {
				t.Fatal(err)
			}
			if _, err := handleUninstall([]string{"--", name}, cwd); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(paths["local"])
			if err != nil {
				t.Fatalf("last-entry removal must retain the file: %v", err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(data, &root); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(root["mcpServers"], &servers); err != nil || servers == nil || len(servers) != 0 {
				t.Fatalf("want mcpServers:{}, got %s (%v)", data, err)
			}
		})
	}
}

func TestUninstallRejectsInvalidArgumentsWithoutWrites(t *testing.T) {
	for _, args := range [][]string{
		{""}, {"two words"}, {" leading"}, {"trailing "}, {"tab\tname"},
		{"line\nname"}, {"carriage\rreturn"}, {"nul\x00name"}, {"del\x7fname"},
		{"escape\x1bname"}, {"unicode\u00a0space"}, {"unicode\u0085control"},
		{"target", "second"}, {"help", "target"}, {"--"}, {"target", "--", "second"},
		{"target", "--", "--global"},
		{"--scope", "local"}, {"--scope=user"}, {"--global"}, {"--project"},
		{"--scope"}, {"-s"}, {"target", "--scope"}, {"target", "-s"},
		{"--scope", "", "target"}, {"--scope=", "target"},
		{"--scope", "--global", "target"}, {"-s", "--project", "target"},
		{"--scope", "invalid", "target"}, {"target", "--scope=global"},
		{"--scope=LOCAL", "target"},
		{"--global=true", "target"}, {"--global=", "target"},
		{"target", "--project=true"}, {"--project=", "target"},
		{"--help=true"}, {"-h=true"},
		{"--unknown", "target"}, {"target", "--unknown"},
		{"--transport", "http", "target"}, {"target", "--env=KEY=value"},
		{"--header=X-Test:value", "target"}, {"--force", "target"},
	} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
					root, cwd, paths := uninstallTestSandbox(t)
					if existing {
						// Invalid names must fail validation, not merely fail because
						// no matching entry happened to exist in the fixture.
						servers := map[string]ServerConfig{"target": {Command: "runner"}}
						for _, arg := range args {
							servers[arg] = ServerConfig{Command: "runner"}
						}
						data, err := json.Marshal(Config{MCPServers: servers})
						if err != nil {
							t.Fatal(err)
						}
						for _, path := range paths {
							uninstallTestWrite(t, path, string(data))
						}
					}
					before := uninstallTestSnapshot(t, root)
					if message, err := handleUninstall(args, cwd); err == nil {
						t.Fatalf("handleUninstall(%q) = %q, want error", args, message)
					}
					if after := uninstallTestSnapshot(t, root); !reflect.DeepEqual(before, after) {
						t.Fatal("invalid arguments modified or created files/directories")
					}
				})
			}
		})
	}
}

func TestUninstallMissingOrMalformedConfigDoesNotWrite(t *testing.T) {
	for _, scope := range []string{"local", "project", "user"} {
		t.Run(scope, func(t *testing.T) {
			for _, content := range []string{
				"missing file", "", " \n\t", `{`, `null`, `[]`, `"string"`, `123`,
				`{"mcpServers": []}`, `{"mcpServers": "not an object"}`, `{"mcpServers": true}`,
				`{"mcpServers": null}`, `{"mcpServers": {}}`, `{"other": {"keep": true}}`,
				`{"mcpServers":{"keeper":{"command":"runner"}}}`,
				`{"mcpServers":{"TARGET":{"command":"runner"}}}`,
				`{"mcpServers":{"target":{"command":"runner"}}} trailing`,
			} {
				t.Run(content, func(t *testing.T) {
					root, cwd, paths := uninstallTestSandbox(t)
					if content != "missing file" {
						uninstallTestWrite(t, paths[scope], content)
					}
					// The name exists everywhere else: never fall back to another scope.
					for other, path := range paths {
						if other != scope {
							uninstallTestWrite(t, path, `{"mcpServers":{"target":{"command":"runner"}}}`)
						}
					}
					for _, path := range globalConfigPaths() {
						if path != paths["user"] {
							uninstallTestWrite(t, path, `{"mcpServers":{"target":{"command":"legacy-runner"}}}`)
						}
					}
					before := uninstallTestSnapshot(t, root)
					if _, err := handleUninstall([]string{"--scope", scope, "target"}, cwd); err == nil {
						t.Fatal("expected missing/malformed configuration or missing entry error")
					}
					if after := uninstallTestSnapshot(t, root); !reflect.DeepEqual(before, after) {
						t.Fatal("failed uninstall modified or created files/directories")
					}
				})
			}
		})
	}
}

func TestUninstallWithoutWorkingDirectory(t *testing.T) {
	root, _, paths := uninstallTestSandbox(t)
	before := uninstallTestSnapshot(t, root)
	for _, args := range [][]string{{"target"}, {"--scope=local", "target"}, {"--project", "target"}} {
		if _, err := handleUninstall(args, ""); err == nil {
			t.Fatalf("handleUninstall(%q, empty cwd) must fail", args)
		}
	}
	if after := uninstallTestSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("missing working directory caused filesystem changes")
	}
	if err := installServerConfig(paths["user"], "target", ServerConfig{Command: "runner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handleUninstall([]string{"--global", "target"}, ""); err != nil {
		t.Fatalf("user scope must not require cwd: %v", err)
	}
}

func TestUninstallPreservesUnknownFieldsAndWritesAtomically(t *testing.T) {
	_, cwd, paths := uninstallTestSandbox(t)
	path := paths["local"]
	const original = `{
  "schemaVersion": 9007199254740993,
  "vendor": {"nested": [null, true, {"extra": "keep"}]},
  "mcpServers": {
    "target": {"command": "remove-me", "unknown": "remove-too"},
    "target-extra": {"command": "runner", "args": ["--flag"], "env": {"TOKEN": "${TOKEN}"}, "vendor": {"large": 9007199254740993}},
    "disabled": {"url": "https://example.com/mcp", "disabled": true, "oauth": {"clientId": "test", "future": [1, 2]}, "unknown": null}
  }
}`
	uninstallTestWrite(t, path, original)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err := handleUninstall([]string{"target"}, cwd); err != nil {
		t.Fatal(err)
	}
	// An already-open reader must still see the original complete file, not an
	// in-place truncation/rewrite of the same inode.
	oldData, err := io.ReadAll(old)
	if err != nil || string(oldData) != original {
		t.Fatalf("uninstall did not atomically replace the file: %q, %v", oldData, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("configuration mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(data []byte) map[string]any {
		t.Helper()
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber() // Unknown numbers must not lose precision.
		var root map[string]any
		if err := decoder.Decode(&root); err != nil {
			t.Fatal(err)
		}
		return root
	}
	want := decode([]byte(original))
	delete(want["mcpServers"].(map[string]any), "target")
	if got := decode(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("uninstall changed fields other than the target entry:\ngot: %#v\nwant: %#v", got, want)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "mcp.json" {
		t.Fatalf("uninstall left temporary files: %v, %v", entries, err)
	}
}

func TestUninstallLeavesCacheAndOAuthCredentialsUntouched(t *testing.T) {
	root, cwd, paths := uninstallTestSandbox(t)
	const resource = "https://example.com/mcp"
	if err := installServerConfig(paths["local"], "target", ServerConfig{URL: resource, Transport: "streamable-http"}); err != nil {
		t.Fatal(err)
	}
	uninstallTestWrite(t, toolCachePath(), `{"version":1,"servers":{"target":{"fingerprint":"keep","tools":[]}}}`)
	uninstallTestWrite(t, oauthStoreFor(resource).path, `{"version":2,"token":{"access_token":"test-only-token"}}`)
	before := uninstallTestSnapshot(t, root)
	if _, err := handleUninstall([]string{"target"}, cwd); err != nil {
		t.Fatal(err)
	}
	after := uninstallTestSnapshot(t, root)
	delete(before, paths["local"])
	delete(after, paths["local"])
	if !reflect.DeepEqual(before, after) {
		t.Fatal("uninstall changed cache, OAuth credentials, or unrelated state")
	}
}

func TestConcurrentInstallAndUninstallPreserveAllOtherEntries(t *testing.T) {
	_, cwd, paths := uninstallTestSandbox(t)
	path := paths["local"]
	const count = 12
	for i := 0; i < count; i++ {
		if err := installServerConfig(path, fmt.Sprintf("old-%d", i), ServerConfig{Command: "old-runner"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := installServerConfig(path, "keeper", ServerConfig{Command: "keep-runner"}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := handleUninstall([]string{fmt.Sprintf("old-%d", i)}, cwd); err != nil {
				t.Errorf("uninstall old-%d: %v", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := installServerConfig(path, fmt.Sprintf("new-%d", i), ServerConfig{Command: "new-runner"}); err != nil {
				t.Errorf("install new-%d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	cfg, err := readConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ServerConfig{"keeper": {Command: "keep-runner"}}
	for i := 0; i < count; i++ {
		want[fmt.Sprintf("new-%d", i)] = ServerConfig{Command: "new-runner"}
	}
	if !reflect.DeepEqual(cfg.MCPServers, want) {
		t.Fatalf("concurrent commands lost or resurrected entries:\ngot: %+v\nwant: %+v", cfg.MCPServers, want)
	}
}
