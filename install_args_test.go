package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitCommandArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", " \t\n\r ", nil},
		{"plain words", "  install\tworker runner\nargument  ", []string{"install", "worker", "runner", "argument"}},
		{"quoted headers", `install -t http -H 'Authorization: Bearer secret token' --header="X-Label: two words" remote https://example.com/mcp`, []string{"install", "-t", "http", "-H", "Authorization: Bearer secret token", "--header=X-Label: two words", "remote", "https://example.com/mcp"}},
		{"quoted env", `install -e 'TOKEN=hello world' --env="QUERY=a=b c" worker runner`, []string{"install", "-e", "TOKEN=hello world", "--env=QUERY=a=b c", "worker", "runner"}},
		{"empty quoted args", `install worker runner -- '' "" end`, []string{"install", "worker", "runner", "--", "", "", "end"}},
		{"empty quoted token", `''`, []string{""}},
		{"adjacent fragments", `pre"two words"'post' a''b ""tail head""`, []string{"pretwo wordspost", "ab", "tail", "head"}},
		{"escaped spaces", `install worker /path\ with\ spaces/runner -- hello\ world`, []string{"install", "worker", "/path with spaces/runner", "--", "hello world"}},
		{"escaped quotes and backslash", `one\"two three\'four back\\slash`, []string{`one"two`, "three'four", `back\slash`}},
		{"double quoted escapes", `"say \"hello\"" "back\\slash"`, []string{`say "hello"`, `back\slash`}},
		{"single quotes literal", `'back\slash "double" $HOME'`, []string{`back\slash "double" $HOME`}},
		{"quoted whitespace", "'line one\nline two' \"tab\there\"", []string{"line one\nline two", "tab\there"}},
		{"no shell expansion", "$MCP_SPLIT_TEST ${MCP_SPLIT_TEST} ~ ~/file *.go $(whoami) `whoami`", []string{"$MCP_SPLIT_TEST", "${MCP_SPLIT_TEST}", "~", "~/file", "*.go", "$(whoami)", "`whoami`"}},
		{"quoted expansion literal", `"$MCP_SPLIT_TEST ${MCP_SPLIT_TEST} $(printf hello)"`, []string{"$MCP_SPLIT_TEST ${MCP_SPLIT_TEST} $(printf hello)"}},
		{"shell punctuation literal", `runner ; | > file # comment`, []string{"runner", ";", "|", ">", "file", "#", "comment"}},
		{"separator stays a token", `install worker runner -- --env=literal --help`, []string{"install", "worker", "runner", "--", "--env=literal", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_SPLIT_TEST", "must not expand")
			got, err := splitCommandArgs(tc.raw)
			if err != nil {
				t.Fatalf("splitCommandArgs(%q): %v", tc.raw, err)
			}
			// Either nil or an empty slice is valid for empty input.
			if len(got) != len(tc.want) || (len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("splitCommandArgs(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSplitCommandArgsRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{
		`'`, `"`, `install worker 'unterminated`, `install worker "unterminated`,
		`install worker runner\`, `install worker "trailing\`,
		`install worker runner "escaped end\"`, `install worker 'mixed"`,
	} {
		t.Run(raw, func(t *testing.T) {
			if args, err := splitCommandArgs(raw); err == nil {
				t.Fatalf("splitCommandArgs(%q) = %#v, expected syntax error", raw, args)
			}
		})
	}
}

func TestSplitCommandArgsInstallRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want ServerConfig
	}{
		{
			"stdio",
			`install --env 'GREETING=hello world' --env="QUERY=a=b c" worker "/path with spaces/runner" -- --child-flag '' "" "two words" escaped\ space '$HOME'`,
			ServerConfig{Transport: "stdio", Command: "/path with spaces/runner", Args: []string{"--child-flag", "", "", "two words", "escaped space", "$HOME"}, Env: map[string]string{"GREETING": "hello world", "QUERY": "a=b c"}},
		},
		{
			"http",
			`install worker --transport=http 'https://example.com/mcp?first=1&second=2' -H 'Authorization: Bearer two words' --header="X-Trace: part:two"`,
			ServerConfig{Transport: "streamable-http", URL: "https://example.com/mcp?first=1&second=2", Headers: map[string]string{"Authorization": "Bearer two words", "X-Trace": "part:two"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("ZOT_HOME", t.TempDir())
			args, err := splitCommandArgs(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) == 0 || args[0] != "install" {
				t.Fatalf("lost command: %q", args)
			}
			if _, err := handleInstall(args[1:], cwd); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfigFile(filepath.Join(cwd, ".zot", "mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.MCPServers["worker"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("quoted install persisted %+v, want %+v", got, tc.want)
			}
		})
	}
}
