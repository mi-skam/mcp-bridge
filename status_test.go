package main

import (
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
	if got, want := s.status(), "grep (authorization required)"; got != want {
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

func TestNotifyLevelWarnOnPartialFailure(t *testing.T) {
	b := &bridge{servers: map[string]*managedServer{}}
	b.servers["grep"] = newManagedServer("grep", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["grep"].state = stateReady
	b.servers["grep"].tools = []mcp.Tool{{Name: "searchGitHub"}}
	b.servers["broken"] = newManagedServer("broken", ServerConfig{}, log.New(io.Discard, "", 0))
	b.servers["broken"].state = stateError
	b.servers["broken"].startErr = errors.New("authorization required\nmore detail")

	if got, want := formatStatusSummary(b), "broken (authorization required) | grep (1 tool)"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := b.notifyLevel(), "warn"; got != want {
		t.Fatalf("notify level = %q, want %q", got, want)
	}
}
