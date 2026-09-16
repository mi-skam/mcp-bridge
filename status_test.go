package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestManagedServerStatusCompact(t *testing.T) {
	s := newManagedServer("grep", ServerConfig{}, "", log.New(io.Discard, "", 0))

	s.state = stateReady
	s.tools = []mcp.Tool{{Name: "searchGitHub"}}
	if got, want := s.status(), "grep: READY — 1 tool discovered"; got != want {
		t.Fatalf("ready status = %q, want %q", got, want)
	}

	s.state = stateStopped
	if got, want := s.status(), "grep: SLEEPING — 1 tool cached, not a live check"; got != want {
		t.Fatalf("stopped status = %q, want %q", got, want)
	}

	s.state = stateError
	s.startErr = errors.New("authorization required")
	if got, want := s.status(), "grep: LOGIN REQUIRED — /mcp login grep"; got != want {
		t.Fatalf("error status = %q, want %q", got, want)
	}
}

func TestFormatStatusSummaryCompact(t *testing.T) {
	b := &bridge{servers: map[string]*managedServer{}}
	b.servers["grep"] = newManagedServer("grep", ServerConfig{}, "", log.New(io.Discard, "", 0))
	b.servers["grep"].state = stateReady
	b.servers["grep"].tools = []mcp.Tool{{Name: "searchGitHub"}}
	b.servers["n8n-mcp"] = newManagedServer("n8n-mcp", ServerConfig{}, "", log.New(io.Discard, "", 0))
	b.servers["n8n-mcp"].state = stateReady
	b.servers["n8n-mcp"].tools = make([]mcp.Tool, 26)

	if got, want := formatStatusSummary(b), "MCP status (last known; not a live health check)\ngrep: READY — 1 tool discovered\nn8n-mcp: READY — 26 tools discovered"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := b.notifyLevel(), "success"; got != want {
		t.Fatalf("notify level = %q, want %q", got, want)
	}
}

func TestManagedServerStopBeforeStartDoesNotPanic(t *testing.T) {
	s := newManagedServer("never-started", ServerConfig{}, "", log.New(io.Discard, "", 0))
	s.stop()
	if got, want := s.state, stateStopped; got != want {
		t.Fatalf("state = %s, want %s", got, want)
	}
}

func TestMCPToolSchemaPreservesExtraFields(t *testing.T) {
	schema := mcpToolSchema(mcp.Tool{
		Name: "query",
		InputSchema: mcp.ToolInputSchema{
			Type:                 "object",
			Properties:           map[string]any{"sql": map[string]any{"type": "string"}},
			Required:             []string{"sql"},
			Defs:                 map[string]any{"Thing": map[string]any{"type": "object"}},
			AdditionalProperties: false,
		},
	})

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if _, ok := got["$defs"]; !ok {
		t.Fatalf("schema did not preserve $defs: %#v", got)
	}
	if got["additionalProperties"] != false {
		t.Fatalf("schema did not preserve additionalProperties=false: %#v", got)
	}
}

func TestCompactErrShortensConnectionFailures(t *testing.T) {
	msg := "initialize: transport error: failed to send request: failed to send request"
	if got, want := compactErr(msg), "connection failed"; got != want {
		t.Fatalf("compactErr = %q, want %q", got, want)
	}
}

func TestNotifyLevelWarnOnPartialFailure(t *testing.T) {
	b := &bridge{servers: map[string]*managedServer{}}
	b.servers["grep"] = newManagedServer("grep", ServerConfig{}, "", log.New(io.Discard, "", 0))
	b.servers["grep"].state = stateReady
	b.servers["grep"].tools = []mcp.Tool{{Name: "searchGitHub"}}
	b.servers["broken"] = newManagedServer("broken", ServerConfig{}, "", log.New(io.Discard, "", 0))
	b.servers["broken"].state = stateError
	b.servers["broken"].startErr = errors.New("authorization required\nmore detail")

	if got, want := formatStatusSummary(b), "MCP status (last known; not a live health check)\nbroken: LOGIN REQUIRED — /mcp login broken\ngrep: READY — 1 tool discovered"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := b.notifyLevel(), "warn"; got != want {
		t.Fatalf("notify level = %q, want %q", got, want)
	}
}

func TestStopDuringStartDiscardsStaleResult(t *testing.T) {
	s := newManagedServer("racy", ServerConfig{}, "", log.New(io.Discard, "", 0))

	// Simulate the window inside start(): state is starting, gen recorded.
	s.mu.Lock()
	s.state = stateStarting
	gen := s.gen
	s.mu.Unlock()

	// stop() arrives while the spawn is in flight.
	s.stop()

	// The in-flight attempt now tries to commit with a stale generation.
	err := s.finishStart(gen, nil, []mcp.Tool{{Name: "late"}}, nil)
	if err == nil {
		t.Fatal("expected error committing a stale start")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateStopped {
		t.Fatalf("state = %s, want stopped", s.state)
	}
	if s.client != nil {
		t.Fatal("stale client must not be installed")
	}
}

func TestCallToolNilClientReturnsErrorNotPanic(t *testing.T) {
	s := newManagedServer("bogus", ServerConfig{Transport: "no-such-transport", RequestTimeout: 1, ConnectTimeout: 1}, "", log.New(io.Discard, "", 0))
	_, err := s.callTool(context.Background(), "anything", nil)
	if err == nil {
		t.Fatal("expected error from unknown transport")
	}
}

func TestPipeStderrKeepsLinesIntactAcrossChunks(t *testing.T) {
	var buf bytes.Buffer
	s := newManagedServer("srv", ServerConfig{}, "", log.New(&buf, "", 0))

	// iotest.OneByteReader forces 1-byte reads: the old 4096-byte
	// chunking code would log every byte as its own "line".
	long := strings.Repeat("x", 100)
	r := iotest.OneByteReader(strings.NewReader("first " + long + "\nsecond line\n"))
	s.pipeStderr(r)

	out := buf.String()
	if !strings.Contains(out, "[srv:stderr] first "+long+"\n") {
		t.Fatalf("first line was split or lost:\n%s", out)
	}
	if !strings.Contains(out, "[srv:stderr] second line\n") {
		t.Fatalf("second line missing:\n%s", out)
	}
	if got := strings.Count(out, "[srv:stderr]"); got != 2 {
		t.Fatalf("logged %d lines, want 2:\n%s", got, out)
	}
}

func TestCompactErrTruncatesOnRuneBoundary(t *testing.T) {
	msg := strings.Repeat("ü", 60) // 120 bytes, 60 runes
	got := compactErr(msg)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}
}

func TestCompactErrClassifications(t *testing.T) {
	cases := []struct{ in, want string }{
		{"context deadline exceeded", "timeout"},
		{"401 Unauthorized", "auth failed"},
		{"exec: \"nope\": executable file not found in $PATH", "command not found"},
		{"initialize: something odd", "initialize failed"},
		{"list tools: something odd", "tool discovery failed"},
		{"", "error"},
	}
	for _, c := range cases {
		if got := compactErr(c.in); got != c.want {
			t.Errorf("compactErr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMCPResultToZot(t *testing.T) {
	if got := mcpResultToZot(nil); len(got.Content) == 0 || got.IsError {
		t.Fatalf("nil result: %+v", got)
	}

	res := &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "hello"}},
		IsError: true,
	}
	got := mcpResultToZot(res)
	if !got.IsError {
		t.Fatal("IsError not propagated")
	}
	if len(got.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(got.Content))
	}

	empty := mcpResultToZot(&mcp.CallToolResult{})
	if len(empty.Content) == 0 {
		t.Fatal("empty MCP result must produce placeholder content")
	}
}
