// mcp-bridge — Connect zot to MCP (Model Context Protocol) servers.
//
// This extension reads MCP server configurations from standard locations
// (same format as Claude Desktop, Cursor, etc.) and bridges their tools
// into zot so the LLM can call them.
//
// Config locations:
//   - Global:  $ZOT_HOME/mcp.json
//   - Project: .zot/mcp.json
//
// Smart lazy: servers are spawned on startup to discover tools, then
// killed after 5 minutes of idle time. On the next tool call, they're
// respawned automatically.
//
// Tool naming: mcp__<server>__<tool>
//
// Slash commands:
//
//	/mcp              — show status of all configured servers
//	/mcp:start <name> — manually start a server
//	/mcp:stop <name>  — manually stop a server
//	/mcp:restart      — restart all servers
//	/mcp:start all    — manually start all servers
//	/mcp:stop all     — manually stop all servers
//
// Build:
//
//	cd extensions/mcp-bridge
//	go build -o mcp-bridge .
//
// Install:
//
//	zot ext install ./mcp-bridge
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/patriceckhart/zot/packages/agent/ext"
)

func main() {
	e := ext.New("mcp", "1.0.0")

	// Logger writes to stderr (captured by zot into ext logs)
	logger := log.New(os.Stderr, "[mcp-bridge] ", log.LstdFlags)

	// Load config
	cwd, _ := os.Getwd()
	cfg, err := loadConfig(cwd)
	if err != nil {
		logger.Printf("config error: %v", err)
		e.Notify("error", "mcp: config error: "+err.Error())
	}

	if len(cfg.MCPServers) == 0 {
		logger.Printf("no MCP servers configured")
		// Still register commands so user can check status
		registerCommands(e, nil)
		e.Run()
		return
	}

	logger.Printf("found %d MCP server(s)", len(cfg.MCPServers))

	// Create bridge
	b := newBridge(e, logger)
	b.loadServers(cfg)

	// Discover and register tools
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := b.discoverAndRegister(ctx); err != nil {
		logger.Printf("discovery error: %v", err)
	}

	// Start idle reaper
	b.startIdleReaper()

	// Register slash commands
	registerCommands(e, b)

	// Notify user after extension is running
	go func() {
		time.Sleep(500 * time.Millisecond) // Wait for hello handshake
		toolCount := 0
		for _, srv := range b.servers {
			srv.mu.Lock()
			toolCount += len(srv.tools)
			srv.mu.Unlock()
		}
		clearExtensionNotes()
		level := b.notifyLevel()
		if toolCount == 0 {
			level = "warn"
		}
		e.Notify(level, formatStatusSummary(b))
	}()

	// Run the extension protocol loop
	if err := e.Run(); err != nil {
		logger.Printf("fatal: %v", err)
	}

	// Cleanup
	b.stopAll()
}

// registerCommands sets up the /mcp slash commands.
func registerCommands(e *ext.Extension, b *bridge) {
	e.Command("mcp", "show MCP server status or manage servers", func(args string) ext.Response {
		args = strings.TrimSpace(args)

		// Parse subcommand
		parts := strings.Fields(args)
		if len(parts) == 0 {
			// /mcp — show status
			if b == nil {
				return ext.Display("mcp: no servers configured")
			}
			return ext.Display(formatStatusSummary(b))
		}

		switch parts[0] {
		case "setup":
			out, err := handleSetup(parts[1:], e.Host().CWD)
			if err != nil {
				return ext.Errorf("%v", err)
			}
			return ext.Display(out)

		case "start":
			return handleStartCommand(e, b, parts[1:])

		case "stop":
			return handleStopCommand(e, b, parts[1:])

		case "restart":
			return handleRestartCommand(e, b)

		default:
			// /mcp <name> — show detailed status for one server
			if b == nil {
				return ext.Errorf("no servers configured")
			}
			name := parts[0]
			srv, ok := b.servers[name]
			if !ok {
				return ext.Errorf("unknown server: %s", name)
			}
			return ext.Display(srv.status())
		}
	})

	e.Command("mcp:start", "start one MCP server, or all MCP servers with 'all'", func(args string) ext.Response {
		return handleStartCommand(e, b, strings.Fields(strings.TrimSpace(args)))
	})

	e.Command("mcp:stop", "stop one MCP server, or all MCP servers with 'all'", func(args string) ext.Response {
		return handleStopCommand(e, b, strings.Fields(strings.TrimSpace(args)))
	})

	e.Command("mcp:restart", "restart all MCP servers", func(args string) ext.Response {
		if strings.TrimSpace(args) != "" {
			return ext.Errorf("usage: /mcp:restart")
		}
		return handleRestartCommand(e, b)
	})

	e.Command("mcp:setup", "add MCP server templates to zot MCP config", func(args string) ext.Response {
		out, err := handleSetup(strings.Fields(strings.TrimSpace(args)), e.Host().CWD)
		if err != nil {
			return ext.Errorf("%v", err)
		}
		return ext.Display(out)
	})
}

func handleStartCommand(e *ext.Extension, b *bridge, args []string) ext.Response {
	if len(args) != 1 {
		return ext.Errorf("usage: /mcp:start <server-name|all>")
	}
	if b == nil {
		return ext.Errorf("no servers configured")
	}
	name := args[0]
	if name == "all" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := b.startAll(ctx); err != nil {
			return ext.Errorf("start all failed: %v", err)
		}
		notifyBridgeStatus(e, b)
		return ext.Noop()
	}
	if err := b.startServer(name); err != nil {
		return ext.Errorf("start %s: %v", name, err)
	}
	notifyBridgeStatus(e, b)
	return ext.Noop()
}

func handleStopCommand(e *ext.Extension, b *bridge, args []string) ext.Response {
	if len(args) != 1 {
		return ext.Errorf("usage: /mcp:stop <server-name|all>")
	}
	if b == nil {
		return ext.Errorf("no servers configured")
	}
	name := args[0]
	if name == "all" {
		b.stopAll()
		notifyBridgeStatus(e, b)
		return ext.Noop()
	}
	if err := b.stopServer(name); err != nil {
		return ext.Errorf("stop %s: %v", name, err)
	}
	notifyBridgeStatus(e, b)
	return ext.Noop()
}

func handleRestartCommand(e *ext.Extension, b *bridge) ext.Response {
	if b == nil {
		return ext.Errorf("no servers configured")
	}
	b.stopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := b.startAll(ctx); err != nil {
		return ext.Errorf("restart failed: %v", err)
	}
	notifyBridgeStatus(e, b)
	return ext.Noop()
}

func notifyBridgeStatus(e *ext.Extension, b *bridge) {
	if e == nil || b == nil {
		return
	}
	clearExtensionNotes()
	e.Notify(b.notifyLevel(), formatStatusSummary(b))
}

func clearExtensionNotes() {
	_, _ = os.Stdout.WriteString("{\"type\":\"clear_notes\"}\n")
}

// formatStatusSummary builds a human-readable status line.
func formatStatusSummary(b *bridge) string {
	lines := b.serverStatus()
	if len(lines) == 0 {
		return "no MCP servers"
	}
	return strings.Join(lines, " | ")
}
