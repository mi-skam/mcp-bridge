package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func installTestPaths(home, cwd string) []string {
	return []string{filepath.Join(cwd, ".zot", "mcp.json"), filepath.Join(cwd, ".mcp.json"), filepath.Join(home, "mcp.json")}
}

func TestHandleInstallScopes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      []string
		pathIndex int
	}{
		{"default local", nil, 0},
		{"local short", []string{"-s", "local"}, 0},
		{"local long", []string{"--scope", "local"}, 0},
		{"local equals", []string{"--scope=local"}, 0},
		{"project short", []string{"-s", "project"}, 1},
		{"project long", []string{"--scope", "project"}, 1},
		{"project equals", []string{"--scope=project"}, 1},
		{"project alias", []string{"--project"}, 1},
		{"user short", []string{"-s", "user"}, 2},
		{"user long", []string{"--scope", "user"}, 2},
		{"user equals", []string{"--scope=user"}, 2},
		{"global alias", []string{"--global"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			paths := installTestPaths(home, cwd)
			args := append(append([]string{}, tc.opts...), "custom-server", "custom-command", "argument")
			out, err := handleInstall(args, cwd)
			if err != nil {
				t.Fatalf("handleInstall(%q): %v", args, err)
			}
			path := paths[tc.pathIndex]
			for _, want := range []string{"custom-server", path, "/reload-ext"} {
				if !strings.Contains(out, want) {
					t.Errorf("install output missing %q: %s", want, out)
				}
			}
			cfg, err := readConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := ServerConfig{Transport: "stdio", Command: "custom-command", Args: []string{"argument"}}
			if len(cfg.MCPServers) != 1 || !reflect.DeepEqual(cfg.MCPServers["custom-server"], want) {
				t.Fatalf("unexpected config: %+v; want %+v", cfg.MCPServers, want)
			}
			for i, other := range paths {
				if i != tc.pathIndex {
					if _, err := os.Stat(other); !os.IsNotExist(err) {
						t.Errorf("install touched wrong scope %s: %v", other, err)
					}
				}
			}
		})
	}
}

func TestHandleInstallStdioArgumentsAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want ServerConfig
	}{
		{
			"intermixed options",
			[]string{"worker", "-e", "TOKEN=hello world", "runner", "first", "--env=EMPTY=", "-t", "stdio", "second", "--env", "QUERY=a=b", "--", "--child-flag", "-e", "--transport=http", "", "--"},
			ServerConfig{Transport: "stdio", Command: "runner", Args: []string{"first", "second", "--child-flag", "-e", "--transport=http", "", "--"}, Env: map[string]string{"TOKEN": "hello world", "EMPTY": "", "QUERY": "a=b"}},
		},
		{
			"separator before name and command",
			[]string{"--transport=stdio", "--", "worker", "runner", "--help", "--global", "--scope", "user", "--header", "X-Test: literal"},
			ServerConfig{Transport: "stdio", Command: "runner", Args: []string{"--help", "--global", "--scope", "user", "--header", "X-Test: literal"}},
		},
		{
			"separator before command",
			[]string{"worker", "--", "runner", "-y", "@vendor/server", "/path with spaces"},
			ServerConfig{Transport: "stdio", Command: "runner", Args: []string{"-y", "@vendor/server", "/path with spaces"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			if _, err := handleInstall(tc.args, cwd); err != nil {
				t.Fatalf("handleInstall(%q): %v", tc.args, err)
			}
			cfg, err := readConfigFile(filepath.Join(cwd, ".zot", "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.MCPServers["worker"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("server = %+v; want %+v", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(home, "mcp.json")); !os.IsNotExist(err) {
				t.Fatalf("child flags changed install scope: %v", err)
			}
		})
	}
}

func TestHandleInstallHTTPTransports(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		transport string
		url       string
	}{
		{"short http", []string{"-t", "http", "remote", "https://example.com/mcp?profile=free", "-H", "Authorization: Bearer a b", "--header", "X-Trace: part:two"}, "streamable-http", "https://example.com/mcp?profile=free"},
		{"long http", []string{"remote", "--transport", "http", "https://example.com/mcp", "--header=Authorization: Bearer a b", "-H", "X-Trace: part:two"}, "streamable-http", "https://example.com/mcp"},
		{"streamable-http", []string{"remote", "http://localhost:8080/mcp", "--transport=streamable-http", "-H", "Authorization: Bearer a b", "--header=X-Trace: part:two"}, "streamable-http", "http://localhost:8080/mcp"},
		{"sse", []string{"--transport=sse", "-H", "Authorization: Bearer a b", "remote", "https://example.com/events", "--header=X-Trace: part:two"}, "sse", "https://example.com/events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("ZOT_HOME", t.TempDir())
			if _, err := handleInstall(tc.args, cwd); err != nil {
				t.Fatalf("handleInstall(%q): %v", tc.args, err)
			}
			cfg, err := readConfigFile(filepath.Join(cwd, ".zot", "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			want := ServerConfig{Transport: tc.transport, URL: tc.url, Headers: map[string]string{"Authorization": "Bearer a b", "X-Trace": "part:two"}}
			if got := cfg.MCPServers["remote"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("server = %+v; want %+v", got, want)
			}
		})
	}
}

func TestHandleInstallNoPresets(t *testing.T) {
	for _, name := range []string{"grep", "filesystem", "context7", "playwright", "you", "arbitrary-server", "add", "templates", "list"} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("ZOT_HOME", t.TempDir())
			if _, err := handleInstall([]string{name}, cwd); err == nil {
				t.Fatal("name alone must require a command or URL, not select a preset")
			}
			if _, err := handleInstall([]string{name, "my-command"}, cwd); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfigFile(filepath.Join(cwd, ".zot", "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			want := ServerConfig{Transport: "stdio", Command: "my-command"}
			if got := cfg.MCPServers[name]; !reflect.DeepEqual(got, want) {
				t.Fatalf("name must not select a preset: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestHandleInstallDuplicatePreservesConfig(t *testing.T) {
	for i, scope := range []string{"local", "project", "user"} {
		t.Run(scope, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			path := installTestPaths(home, cwd)[i]
			original := []byte("{\n  \"customRoot\": true,\n  \"mcpServers\": {\n    \"existing\": {\"command\": \"keep\", \"customField\": [1, 2]},\n    \"other\": {\"url\": \"https://example.com/mcp\"}\n  }\n}\n")
			writeJSON(t, path, string(original))
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := handleInstall([]string{"--scope", scope, "existing", "replacement"}, cwd); err == nil {
				t.Fatal("expected duplicate server error")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, original) {
				t.Fatalf("duplicate install changed config:\n%s", data)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("duplicate install must not rewrite even unchanged config")
			}
		})
	}
}

func TestHandleInstallPreservesUnknownConfigFields(t *testing.T) {
	for i, scope := range []string{"local", "project", "user"} {
		t.Run(scope, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			path := installTestPaths(home, cwd)[i]
			const original = `{"$schema":"https://example.com/schema.json","customRoot":{"enabled":true,"values":[1,"two",null]},"mcpServers":{"existing":{"command":"keep","args":["a"],"env":{"TOKEN":"secret"},"disabled":true,"futureOption":{"nested":[true,null,3]},"description":"preserve me"},"remote":{"transport":"sse","url":"https://example.com/events","headers":{"X-Key":"keep"},"vendorField":42}}}`
			writeJSON(t, path, original)
			if _, err := handleInstall([]string{"--scope", scope, "new-server", "runner"}, cwd); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if err := json.Unmarshal([]byte(original), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &after); err != nil {
				t.Fatal(err)
			}
			servers, ok := after["mcpServers"].(map[string]any)
			if !ok {
				t.Fatalf("missing mcpServers: %s", data)
			}
			want := map[string]any{"transport": "stdio", "command": "runner"}
			if !reflect.DeepEqual(servers["new-server"], want) {
				t.Fatalf("new server = %#v; want %#v", servers["new-server"], want)
			}
			delete(servers, "new-server")
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("install changed existing root or server fields:\nbefore: %s\nafter: %s", original, data)
			}
		})
	}
}

func TestInstallHelp(t *testing.T) {
	help := installHelp()
	for _, want := range []string{"/mcp install", "<name>", "<commandOrUrl>", "--transport", "--env", "--header", "--scope", "--global", "--project", "local", "project", "user"} {
		if !strings.Contains(help, want) {
			t.Errorf("install help missing %q:\n%s", want, help)
		}
	}
	for _, obsolete := range []string{"/mcp setup", "/mcp install add", "/mcp install templates", "/mcp install list", "<template>", "--name", "Available servers:", "Install a known MCP server"} {
		if strings.Contains(help, obsolete) {
			t.Errorf("install help advertises obsolete syntax %q", obsolete)
		}
	}
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("ZOT_HOME", home)
			out, err := handleInstall(args, cwd)
			if err != nil || out != help {
				t.Fatalf("handleInstall(%q) = %q, %v; want install help", args, out, err)
			}
			for _, path := range installTestPaths(home, cwd) {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("help must not create config %s: %v", path, err)
				}
			}
		})
	}
}

func TestHandleInstallRejectsInvalidArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing target", []string{"server"}},
		{"URL requires explicit HTTP transport", []string{"worker", "https://example.com/mcp"}},
		{"only options", []string{"--scope", "user"}},
		{"empty name", []string{"", "runner"}},
		{"space in name", []string{"bad name", "runner"}},
		{"tab in name", []string{"bad\tname", "runner"}},
		{"newline in name", []string{"bad\nname", "runner"}},
		{"unicode whitespace in name", []string{"bad\u00a0name", "runner"}},
		{"empty command", []string{"server", ""}},
		{"unknown option", []string{"server", "runner", "--unknown"}},
		{"child flag without separator", []string{"server", "npx", "-y", "package"}},
		{"removed name flag", []string{"server", "runner", "--name", "other"}},
		{"removed name equals flag", []string{"server", "runner", "--name=other"}},
		{"unknown transport", []string{"-t", "websocket", "server", "https://example.com"}},
		{"empty transport", []string{"--transport=", "server", "runner"}},
		{"unknown scope", []string{"--scope", "global", "server", "runner"}},
		{"empty scope", []string{"--scope=", "server", "runner"}},
		{"global value", []string{"--global=true", "server", "runner"}},
		{"project value", []string{"--project=true", "server", "runner"}},
		{"env missing equals", []string{"server", "runner", "-e", "TOKEN"}},
		{"env missing key", []string{"server", "runner", "--env==value"}},
		{"empty env", []string{"server", "runner", "--env="}},
		{"env key whitespace", []string{"server", "runner", "-e", "BAD KEY=value"}},
		{"stdio header", []string{"server", "runner", "-H", "X-Key: value"}},
		{"http env", []string{"-t", "http", "server", "https://example.com", "-e", "TOKEN=value"}},
		{"sse env", []string{"-t", "sse", "server", "https://example.com", "-e", "TOKEN=value"}},
		{"http extra arg", []string{"-t", "http", "server", "https://example.com", "extra"}},
		{"sse extra arg", []string{"-t", "sse", "server", "https://example.com", "--", "extra"}},
		{"http empty arg", []string{"-t", "http", "server", "https://example.com", "--", ""}},
		{"header missing colon", []string{"-t", "http", "server", "https://example.com", "-H", "X-Key=value"}},
		{"header missing name", []string{"-t", "http", "server", "https://example.com", "-H", ": value"}},
		{"empty header", []string{"-t", "http", "server", "https://example.com", "--header="}},
		{"header invalid name", []string{"-t", "http", "server", "https://example.com", "-H", "Bad Name: value"}},
		{"header newline", []string{"-t", "http", "server", "https://example.com", "-H", "X-Key: value\r\nInjected: yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testInstallRejectedUnchanged(t, tc.args)
		})
	}
	for _, flag := range []string{"-t", "--transport", "-s", "--scope", "-e", "--env", "-H", "--header"} {
		t.Run("missing value "+flag, func(t *testing.T) {
			testInstallRejectedUnchanged(t, []string{"server", "runner", flag})
		})
	}
	for _, transport := range []string{"http", "streamable-http", "sse"} {
		for _, target := range []string{"", "runner", "/relative", "ftp://example.com/mcp", "https:///mcp", "https://", "http://?query=value", "https://example.com/%zz", "https://bad host/mcp"} {
			t.Run(transport+" invalid URL "+target, func(t *testing.T) {
				testInstallRejectedUnchanged(t, []string{"--transport", transport, "server", target})
			})
		}
	}
}

func testInstallRejectedUnchanged(t *testing.T, args []string) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("ZOT_HOME", home)
	paths := installTestPaths(home, cwd)
	const original = `{"customRoot":true,"mcpServers":{"existing":{"command":"keep","unknown":"preserve"}}}`
	for _, path := range paths {
		writeJSON(t, path, original)
	}
	if _, err := handleInstall(args, cwd); err == nil {
		t.Fatalf("handleInstall(%q): expected error", args)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != original {
			t.Errorf("invalid arguments modified %s: %s", path, data)
		}
	}
}
