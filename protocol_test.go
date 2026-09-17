package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
)

// inMemoryBridge wires one managedServer to an in-process go-sdk server that
// exposes a tool, a resource, a resource template and a prompt.
func inMemoryBridge(t *testing.T) *bridge {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo text."}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})
	srv.AddResource(&mcp.Resource{URI: "mem://notes/today", Name: "today", MIMEType: "text/plain"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "text/plain", Text: "buy milk"}}}, nil
	})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "mem://notes/{day}", Name: "note-by-day"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, Text: "note for " + strings.TrimPrefix(r.Params.URI, "mem://notes/")}}}, nil
	})
	srv.AddPrompt(&mcp.Prompt{Name: "greet", Description: "Greet someone.", Arguments: []*mcp.PromptArgument{{Name: "who", Required: true}}}, func(_ context.Context, r *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hello, " + r.Params.Arguments["who"]}}}}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	ms := newManagedServer("fx", ServerConfig{RequestTimeout: 5}, "", log.New(io.Discard, "", 0))
	ms.client, ms.state = cs, stateReady
	return &bridge{servers: map[string]*managedServer{"fx": ms}, mapping: map[string]toolMapping{}}
}

func text(r ext.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func TestProtocolTools(t *testing.T) {
	b := inMemoryBridge(t)
	call := func(fn func(json.RawMessage) ext.ToolResult, args string) ext.ToolResult {
		return fn(json.RawMessage(args))
	}

	if r := call(b.callTool, `{"server":"fx","tool":"echo","args":{"text":"hi"}}`); r.IsError || text(r) != "hi" {
		t.Fatalf("mcp__call: %+v", r)
	}
	if r := call(b.callTool, `{"server":"nope","tool":"echo"}`); !r.IsError || !strings.Contains(text(r), "Configured: fx") {
		t.Fatalf("unknown server must list configured ones: %+v", r)
	}
	if r := call(b.describeTool, `{"server":"fx"}`); r.IsError || !strings.Contains(text(r), "- echo: Echo text.") {
		t.Fatalf("describe list: %+v", r)
	}
	r := call(b.describeTool, `{"server":"fx","tool":"echo"}`)
	var desc map[string]any
	if r.IsError || json.Unmarshal([]byte(text(r)), &desc) != nil || desc["zotTool"] != "mcp__fx__echo" || desc["inputSchema"] == nil {
		t.Fatalf("describe one: %+v", r)
	}
	if r := call(b.resourcesTool, `{"server":"fx","action":"list"}`); r.IsError || !strings.Contains(text(r), "mem://notes/today (today) [text/plain]") || !strings.Contains(text(r), "template mem://notes/{day}") {
		t.Fatalf("resources list: %+v", r)
	}
	if r := call(b.resourcesTool, `{"server":"fx","action":"read","uri":"mem://notes/2026-09-17"}`); r.IsError || text(r) != "note for 2026-09-17" {
		t.Fatalf("resources read via template: %+v", r)
	}
	if r := call(b.promptsTool, `{"server":"fx","action":"list"}`); r.IsError || !strings.Contains(text(r), "- greet: Greet someone.\n    who, required:") {
		t.Fatalf("prompts list: %+v", r)
	}
	if r := call(b.promptsTool, `{"server":"fx","action":"get","name":"greet","args":{"who":"zot"}}`); r.IsError || text(r) != "user: Hello, zot" {
		t.Fatalf("prompts get: %+v", r)
	}
	if r := call(b.resourcesTool, `{"server":"fx","action":"delete"}`); !r.IsError {
		t.Fatal("bad action must error")
	}
}

// A server that adds a tool at runtime triggers tools/list_changed; the bridge
// must record it and run the refresh hook, and mcp__describe must see the new
// tool without any reload.
func TestToolsListChangedNotification(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "one", Description: "first"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	ms := newManagedServer("fx", ServerConfig{RequestTimeout: 5}, "", log.New(io.Discard, "", 0))
	changed := make(chan string, 1)
	ms.onToolsChanged = func(name string) { changed <- name }
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, ms.clientOptions()).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ms.client, ms.state = cs, stateReady

	mcp.AddTool(srv, &mcp.Tool{Name: "two", Description: "second"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	select {
	case name := <-changed:
		if name != "fx" {
			t.Fatalf("hook got %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tools/list_changed hook not called")
	}
	if !strings.Contains(ms.detailStatus(0), "TOOLS CHANGED") {
		t.Fatal("notification missing from lifecycle log")
	}
	b := &bridge{servers: map[string]*managedServer{"fx": ms}}
	if r := b.describeTool(json.RawMessage(`{"server":"fx"}`)); !strings.Contains(text(r), "- two: second") {
		t.Fatalf("describe must see the new tool live: %s", text(r))
	}
}
