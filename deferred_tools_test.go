package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/patriceckhart/zot/packages/agent/ext"
	"github.com/patriceckhart/zot/packages/agent/extproto"
)

type bridgeExtensionHarness struct {
	ext    *ext.Extension
	hostW  *os.File
	frames chan []byte
}

func newBridgeExtensionHarness(t *testing.T) *bridgeExtensionHarness {
	t.Helper()
	extIn, hostW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	hostR, extOut, err := os.Pipe()
	if err != nil {
		extIn.Close()
		hostW.Close()
		t.Fatal(err)
	}

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = extIn, extOut
	e := ext.New("mcp-test", "0.0.0-test")
	os.Stdin, os.Stdout = oldIn, oldOut

	h := &bridgeExtensionHarness{ext: e, hostW: hostW, frames: make(chan []byte, 32)}
	go func() {
		scanner := bufio.NewScanner(hostR)
		for scanner.Scan() {
			h.frames <- append([]byte(nil), scanner.Bytes()...)
		}
		close(h.frames)
	}()
	t.Cleanup(func() {
		hostW.Close()
		extIn.Close()
		extOut.Close()
		hostR.Close()
	})
	return h
}

func (h *bridgeExtensionHarness) next(t *testing.T) []byte {
	t.Helper()
	select {
	case frame, ok := <-h.frames:
		if !ok {
			t.Fatal("extension output closed")
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for extension frame")
		return nil
	}
}

func (h *bridgeExtensionHarness) send(t *testing.T, frame any) {
	t.Helper()
	data, err := extproto.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.hostW.Write(data); err != nil {
		t.Fatal(err)
	}
}

func (h *bridgeExtensionHarness) startAndCollectTools(t *testing.T) []extproto.RegisterToolFromExt {
	t.Helper()
	go func() { _ = h.ext.Run() }()

	var hello extproto.Frame
	if err := json.Unmarshal(h.next(t), &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Type != "hello" {
		t.Fatalf("first extension frame = %q, want hello", hello.Type)
	}
	h.send(t, extproto.HelloAckFromHost{Type: "hello_ack", ProtocolVersion: extproto.ProtocolVersion})

	var tools []extproto.RegisterToolFromExt
	for {
		frame := h.next(t)
		var header extproto.Frame
		if err := json.Unmarshal(frame, &header); err != nil {
			t.Fatal(err)
		}
		switch header.Type {
		case "register_tool":
			var tool extproto.RegisterToolFromExt
			if err := json.Unmarshal(frame, &tool); err != nil {
				t.Fatal(err)
			}
			tools = append(tools, tool)
		case "ready":
			return tools
		}
	}
}

func (h *bridgeExtensionHarness) callTool(t *testing.T, id, name, args string) extproto.ToolResultFromExt {
	t.Helper()
	h.send(t, extproto.ToolCallFromHost{
		Type: "tool_call",
		ID:   id,
		Name: name,
		Args: json.RawMessage(args),
	})
	for {
		frame := h.next(t)
		var header extproto.Frame
		if err := json.Unmarshal(frame, &header); err != nil {
			t.Fatal(err)
		}
		if header.Type != "tool_result" || header.ID != id {
			continue
		}
		var result extproto.ToolResultFromExt
		if err := json.Unmarshal(frame, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
}

func cachedDiscoveryFixture(t *testing.T, e *ext.Extension) (*bridge, toolCache) {
	t.Helper()
	b := newBridge(e, "/project", log.New(io.Discard, "", 0))
	b.loadServers(Config{MCPServers: map[string]ServerConfig{
		"alpha": {Transport: "stdio", Command: "alpha-server"},
		"beta":  {Transport: "stdio", Command: "beta-server"},
	}})
	return b, toolCache{Version: toolCacheVersion, Servers: map[string]cachedServer{
		"alpha": {
			Fingerprint: serverFingerprint(b.servers["alpha"].config),
			Tools: []cachedTool{
				{Name: "repo_lookup", Description: "Find repository metadata", Schema: json.RawMessage(`{"type":"object"}`)},
				{Name: "issue_scan", Description: "Search repository issues", Schema: json.RawMessage(`{"type":"object"}`)},
				{Name: "deploy", Description: "Ship a release", Schema: json.RawMessage(`{"type":"object"}`)},
			},
		},
		"beta": {
			Fingerprint: serverFingerprint(b.servers["beta"].config),
			Tools: []cachedTool{
				{Name: "repository_stats", Description: "Count projects", Schema: json.RawMessage(`{"type":"object"}`)},
				{Name: "code_search", Description: "Find source snippets", Schema: json.RawMessage(`{"type":"object"}`)},
			},
		},
	}}
}

func TestCachedMCPToolsAreDeferredWithOneActiveLoader(t *testing.T) {
	h := newBridgeExtensionHarness(t)
	b, cache := cachedDiscoveryFixture(t, h.ext)
	b.registerToolSearch()
	if got := b.registerCachedTools(cache); got != 5 {
		t.Fatalf("registerCachedTools() = %d, want 5", got)
	}

	registrations := h.startAndCollectTools(t)
	var active []string
	deferred := map[string]bool{}
	for _, registration := range registrations {
		if registration.Deferred {
			deferred[registration.Name] = true
		} else {
			active = append(active, registration.Name)
		}
	}
	wantDeferred := []string{
		"mcp__alpha__deploy",
		"mcp__alpha__issue_scan",
		"mcp__alpha__repo_lookup",
		"mcp__beta__code_search",
		"mcp__beta__repository_stats",
	}
	for _, name := range wantDeferred {
		if !deferred[name] {
			t.Errorf("cached MCP tool %q was not registered as deferred", name)
		}
	}
	if !reflect.DeepEqual(active, []string{mcpSearchToolName}) {
		t.Fatalf("active tools = %v, want only %s", active, mcpSearchToolName)
	}
}

func TestMCPToolSearchMatchesNameAndDescriptionDeterministically(t *testing.T) {
	h := newBridgeExtensionHarness(t)
	b, cache := cachedDiscoveryFixture(t, h.ext)
	b.registerToolSearch()
	b.registerCachedTools(cache)
	registrations := h.startAndCollectTools(t)

	loader := ""
	for _, registration := range registrations {
		if !registration.Deferred {
			loader = registration.Name
		}
	}
	if loader == "" {
		t.Fatal("no active MCP tool loader registered")
	}

	byName := h.callTool(t, "name", loader, `{"query":"repository_stats"}`)
	if byName.IsError || !reflect.DeepEqual(byName.ActivateTools, []string{"mcp__beta__repository_stats"}) {
		t.Fatalf("name search result: error=%v activate_tools=%v", byName.IsError, byName.ActivateTools)
	}

	byDescription := h.callTool(t, "description", loader, `{"query":"source snippets"}`)
	if byDescription.IsError || !reflect.DeepEqual(byDescription.ActivateTools, []string{"mcp__beta__code_search"}) {
		t.Fatalf("description search result: error=%v activate_tools=%v", byDescription.IsError, byDescription.ActivateTools)
	}

	wantLimited := []string{"mcp__beta__repository_stats", "mcp__alpha__issue_scan"}
	first := h.callTool(t, "limited-1", loader, `{"query":"repository","limit":2}`)
	second := h.callTool(t, "limited-2", loader, `{"query":"repository","limit":2}`)
	if first.IsError || !reflect.DeepEqual(first.ActivateTools, wantLimited) {
		t.Fatalf("limited search activate_tools = %v, want %v", first.ActivateTools, wantLimited)
	}
	if !reflect.DeepEqual(second.ActivateTools, first.ActivateTools) {
		t.Fatalf("repeated search was not deterministic: first=%v second=%v", first.ActivateTools, second.ActivateTools)
	}
}

func TestMCPToolSearchEmptyAndNoMatchAreSafe(t *testing.T) {
	h := newBridgeExtensionHarness(t)
	b, cache := cachedDiscoveryFixture(t, h.ext)
	b.registerToolSearch()
	b.registerCachedTools(cache)
	registrations := h.startAndCollectTools(t)

	loader := ""
	for _, registration := range registrations {
		if !registration.Deferred {
			loader = registration.Name
		}
	}
	if loader == "" {
		t.Fatal("no active MCP tool loader registered")
	}

	empty := h.callTool(t, "empty", loader, `{}`)
	if len(empty.ActivateTools) != 0 {
		t.Errorf("empty search activated tools: %v", empty.ActivateTools)
	}

	noMatch := h.callTool(t, "no-match", loader, `{"query":"definitely-not-an-mcp-tool"}`)
	if noMatch.IsError {
		t.Error("no-match search returned an error")
	}
	if len(noMatch.ActivateTools) != 0 {
		t.Errorf("no-match search activated tools: %v", noMatch.ActivateTools)
	}
}
