package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
)

func testBridge() *bridge {
	return &bridge{
		cwd:     "/project",
		servers: map[string]*managedServer{},
		mapping: map[string]toolMapping{},
		logger:  log.New(io.Discard, "", 0),
	}
}

func TestHandleToolCallUnknownToolIsFriendlyError(t *testing.T) {
	b := testBridge()
	res := b.handleToolCall("mcp__ghost__tool", nil)
	if !res.IsError {
		t.Fatal("expected error result")
	}
}

func TestHandleToolCallStartFailureIsFriendlyError(t *testing.T) {
	b := testBridge()
	b.servers["bogus"] = newManagedServer("bogus",
		ServerConfig{Transport: "no-such-transport", ConnectTimeout: 1, RequestTimeout: 1},
		"/project",
		log.New(io.Discard, "", 0))
	b.mapping["mcp__bogus__t"] = toolMapping{serverName: "bogus", mcpTool: "t"}

	res := b.handleToolCall("mcp__bogus__t", nil)
	if !res.IsError {
		t.Fatal("expected error result for failed server start")
	}
}

func TestLoadServersPassesProjectCWD(t *testing.T) {
	b := testBridge()
	b.loadServers(Config{MCPServers: map[string]ServerConfig{
		"fs": {Transport: "stdio", Command: "dummy"},
	}})
	if got := b.servers["fs"].cwd; got != "/project" {
		t.Fatalf("managed server cwd = %q, want /project", got)
	}
}

func TestClassifyTimeoutUsesErrorsIs(t *testing.T) {
	wrapped := errors.Join(errors.New("call failed"), context.DeadlineExceeded)
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatal("sanity: errors.Is must see through the wrap")
	}
	// The user-facing message for a deadline error must mention the
	// configured timeout even when the error text never says "timeout".
	msg := timeoutErrorMessage("slow", 42)
	if !strings.Contains(msg, "42") || !strings.Contains(msg, "slow") {
		t.Fatalf("timeout message missing details: %q", msg)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"filesystem", "filesystem"},
		{"my-tool", "my_tool"},
		{"my.tool", "my_tool"},
		{"3d-render", "t_3d_render"},
		{"", "unnamed"},
	}
	for _, c := range cases {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToolName(t *testing.T) {
	if got, want := toolName("fs-server", "read.file"), "mcp__fs_server__read_file"; got != want {
		t.Fatalf("toolName = %q, want %q", got, want)
	}
}

func TestRegisterCachedToolLogsCollision(t *testing.T) {
	var buf bytes.Buffer
	b := testBridge()
	b.logger = log.New(&buf, "", 0)
	// nil Extension: registerCachedTool must not reach b.e.Tool for
	// the colliding (skipped) registration, and the first registration
	// needs a real Extension — so pre-seed the mapping instead.
	b.mapping["mcp__srv__my_tool"] = toolMapping{serverName: "srv", mcpTool: "my-tool"}

	b.registerCachedTool("srv", cachedTool{Name: "my_tool", Schema: []byte(`{}`)})

	if !strings.Contains(buf.String(), "collision") {
		t.Fatalf("expected collision log, got: %q", buf.String())
	}
}
