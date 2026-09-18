package main

import (
	"encoding/json"
	"io"
	"log"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
	"github.com/patriceckhart/zot/packages/agent/extproto"
)

// These fixtures only describe last-known state; no config is loaded and no
// subprocess or MCP connection is started.
func commandStatusFixture(t *testing.T) *bridge {
	t.Helper()
	b := newBridge(nil, t.TempDir(), log.New(io.Discard, "", 0))
	b.servers["alpha"] = newManagedServer("alpha", ServerConfig{Command: "unused-test-command"}, b.cwd, b.logger)
	b.servers["alpha"].tools = []*mcp.Tool{{Name: "first"}, {Name: "second"}}
	b.servers["alpha"].recent = []string{"12:00:00 STOPPED"}
	b.servers["beta"] = newManagedServer("beta", ServerConfig{Disabled: true}, b.cwd, b.logger)
	b.mapping["mcp__alpha__first"] = toolMapping{serverName: "alpha", mcpTool: "first"}
	b.mapping["mcp__beta__other"] = toolMapping{serverName: "beta", mcpTool: "other"}
	return b
}

func assertStatusCommandResponse(t *testing.T, got ext.Response, wantError string) {
	t.Helper()
	// SDK errors are noop responses with Error set, not Action == "error".
	if got != (ext.Response{Action: "noop", Error: wantError}) {
		t.Fatalf("response = %+v, want noop with error %q", got, wantError)
	}
}

func TestHandleListCommand(t *testing.T) {
	for _, fixture := range []struct {
		name string
		b    *bridge
	}{
		{"nil bridge", nil},
		{"empty bridge", newBridge(nil, t.TempDir(), log.New(io.Discard, "", 0))},
		{"configured bridge", commandStatusFixture(t)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				args      []string
				wantError string
			}{
				{"no args", nil, ""},
				{"empty args", []string{}, ""},
				{"server arg", []string{"alpha"}, "usage: /mcp list"},
				{"extra args", []string{"alpha", "beta"}, "usage: /mcp list"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					assertStatusCommandResponse(t, handleListCommand(nil, fixture.b, tc.args), tc.wantError)
				})
			}
		})
	}
}

func TestHandleStatusCommand(t *testing.T) {
	configured := commandStatusFixture(t)
	empty := newBridge(nil, t.TempDir(), log.New(io.Discard, "", 0))
	for _, tc := range []struct {
		name      string
		b         *bridge
		args      []string
		wantError string
	}{
		{"missing name without bridge", nil, nil, "usage: /mcp status <server>"},
		{"missing name with empty bridge", empty, nil, "usage: /mcp status <server>"},
		{"missing name with servers", configured, nil, "usage: /mcp status <server>"},
		{"extra name without bridge", nil, []string{"alpha", "beta"}, "usage: /mcp status <server>"},
		{"extra name with empty bridge", empty, []string{"alpha", "beta"}, "usage: /mcp status <server>"},
		{"extra name with servers", configured, []string{"alpha", "beta"}, "usage: /mcp status <server>"},
		{"no configured servers", nil, []string{"alpha"}, "no servers configured"},
		{"empty server map", empty, []string{"alpha"}, "unknown server: alpha"},
		{"unknown server", configured, []string{"missing"}, "unknown server: missing"},
		{"known server", configured, []string{"alpha"}, ""},
		{"disabled server", configured, []string{"beta"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertStatusCommandResponse(t, handleStatusCommand(nil, tc.b, tc.args), tc.wantError)
		})
	}
}

func TestMCPListAndStatusCommandRouting(t *testing.T) {
	b := commandStatusFixture(t)
	for _, tc := range []struct {
		name        string
		b           *bridge
		args        string
		wantError   string
		wantMessage string
		wantLevel   string
	}{
		{"list without servers", nil, "list", "", mcpOverview(nil), "info"},
		{"list all servers", b, "list", "", mcpOverview(b), "info"},
		{"list rejects server", b, "list alpha", "usage: /mcp list", "", ""},
		{"list validates before nil bridge", nil, "list alpha", "usage: /mcp list", "", ""},
		{"status requires name", b, "status", "usage: /mcp status <server>", "", ""},
		{"status validates before nil bridge", nil, "status", "usage: /mcp status <server>", "", ""},
		{"status rejects extra name", b, "status alpha beta", "usage: /mcp status <server>", "", ""},
		{"status without servers", nil, "status alpha", "no servers configured", "", ""},
		{"status unknown server", b, "status missing", "unknown server: missing", "", ""},
		{"status details", b, "status alpha", "", b.servers["alpha"].detailStatus(1), "info"},
		{"server shortcut unchanged", b, "alpha", "", b.servers["alpha"].detailStatus(1), "info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBridgeExtensionHarness(t)
			registerCommands(h.ext, tc.b)
			h.startAndCollectTools(t)
			h.send(t, extproto.CommandInvokedFromHost{
				Type: "command_invoked", ID: "inspect", Name: "mcp", Args: tc.args,
			})
			var notifications []extproto.NotifyFromExt
			for {
				frame := h.next(t)
				var header extproto.Frame
				if err := json.Unmarshal(frame, &header); err != nil {
					t.Fatal(err)
				}
				switch header.Type {
				case "notify":
					var note extproto.NotifyFromExt
					if err := json.Unmarshal(frame, &note); err != nil {
						t.Fatal(err)
					}
					notifications = append(notifications, note)
				case "command_response":
					var response extproto.CommandResponseFromExt
					if err := json.Unmarshal(frame, &response); err != nil {
						t.Fatal(err)
					}
					if response.ID != "inspect" || response.Action != "noop" || response.Error != tc.wantError {
						t.Fatalf("response = %+v, want inspect noop with error %q", response, tc.wantError)
					}
					if tc.wantMessage == "" {
						if len(notifications) != 0 {
							t.Fatalf("invalid command emitted notifications: %+v", notifications)
						}
					} else if len(notifications) != 1 || notifications[0].Message != tc.wantMessage || notifications[0].Level != tc.wantLevel {
						t.Fatalf("notifications = %+v, want one %s notification:\n%s", notifications, tc.wantLevel, tc.wantMessage)
					}
					return
				}
			}
		})
	}
}
