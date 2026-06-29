package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestManagedServerStatusCompact(t *testing.T) {
	s := newManagedServer("grep", ServerConfig{}, log.New(io.Discard, "", 0))

	s.state = stateReady
	s.tools = []mcp.Tool{{Name: "searchGitHub"}}
	if got, want := s.status(), "grep (1 tool)"; got != want {
		t.Fatalf("ready status = %q, want %q", got, want)
	}

	s.state = stateStopped
	if got, want := s.status(), "grep (sleeping, 1 tool)"; got != want {
		t.Fatalf("stopped status = %q, want %q", got, want)
	}

	s.state = stateError
	s.startErr = errors.New("authorization required")
	if got, want := s.status(), "grep (auth failed)"; got != want {
		t.Fatalf("error status = %q, want %q", got, want)
	}
}

func TestFormatStatusSummaryCompact(t *testing.T) {
	b := &bridge{servers: map[string]*managedServer{}}
	b.servers["grep"] = newManagedServer("grep", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["grep"].state = stateReady
	b.servers["grep"].tools = []mcp.Tool{{Name: "searchGitHub"}}
	b.servers["n8n-mcp"] = newManagedServer("n8n-mcp", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["n8n-mcp"].state = stateReady
	b.servers["n8n-mcp"].tools = make([]mcp.Tool, 26)

	if got, want := formatStatusSummary(b), "grep (1 tool) | n8n-mcp (26 tools)"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := b.notifyLevel(), "success"; got != want {
		t.Fatalf("notify level = %q, want %q", got, want)
	}
}

func TestManagedServerStopBeforeStartDoesNotPanic(t *testing.T) {
	s := newManagedServer("never-started", ServerConfig{}, log.New(io.Discard, "", 0))
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
	b.servers["grep"] = newManagedServer("grep", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["grep"].state = stateReady
	b.servers["grep"].tools = []mcp.Tool{{Name: "searchGitHub"}}
	b.servers["broken"] = newManagedServer("broken", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["broken"].state = stateError
	b.servers["broken"].startErr = errors.New("authorization required\nmore detail")

	if got, want := formatStatusSummary(b), "broken (auth failed) | grep (1 tool)"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := b.notifyLevel(), "warn"; got != want {
		t.Fatalf("notify level = %q, want %q", got, want)
	}
}
