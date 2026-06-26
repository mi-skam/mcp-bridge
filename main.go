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
//   /mcp              — show status of all configured servers
//   /mcp:start <name> — manually start a server
//   /mcp:stop <name>  — manually stop a server
//   /mcp:restart      — restart all servers
//
// Build:
//
//	cd examples/extensions/mcp-bridge
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
	e := ext.New("mcp-bridge", "1.0.0")

	// Logger writes to stderr (captured by zot into ext logs)
	logger := log.New(os.Stderr, "[mcp-bridge] ", log.LstdFlags)

	// Load config
	cwd, _ := os.Getwd()
	cfg, err := loadConfig(cwd)
	if err != nil {
		logger.Printf("config error: %v", err)
		e.Notify("error", "mcp-bridge: config error: "+err.Error())
	}

	if len(cfg.MCPServers) == 0 {
		logger.Printf("no MCP servers configured")
		// Still register commands so user can check status
		registerCommands(e, nil, logger)
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
	registerCommands(e, b, logger)

	// Notify user after extension is running
	go func() {
		time.Sleep(500 * time.Millisecond) // Wait for hello handshake
		toolCount := 0
		for _, srv := range b.servers {
			srv.mu.Lock()
			toolCount += len(srv.tools)
			srv.mu.Unlock()
		}
		if toolCount > 0 {
			e.Notify("success", formatStatusSummary(b))
		} else {
			e.Notify("warn", "mcp-bridge: no tools discovered (check logs)")
		}
	}()

	// Run the extension protocol loop
	if err := e.Run(); err != nil {
		logger.Printf("fatal: %v", err)
	}

	// Cleanup
	b.stopAll()
}

// registerCommands sets up the /mcp slash commands.
func registerCommands(e *ext.Extension, b *bridge, logger *log.Logger) {
	e.Command("mcp", "show MCP server status or manage servers", func(args string) ext.Response {
		args = strings.TrimSpace(args)

		// Parse subcommand
		parts := strings.Fields(args)
		if len(parts) == 0 {
			// /mcp — show status
			if b == nil {
				return ext.Display("mcp-bridge: no servers configured")
			}
			return ext.Display(formatStatusSummary(b))
		}

		switch parts[0] {
		case "start":
			if len(parts) < 2 {
				return ext.Errorf("usage: /mcp:start <server-name>")
			}
			if b == nil {
				return ext.Errorf("no servers configured")
			}
			name := parts[1]
			if err := b.startServer(name); err != nil {
				return ext.Errorf("start %s: %v", name, err)
			}
			return ext.Display("started server: " + name)

		case "stop":
			if len(parts) < 2 {
				return ext.Errorf("usage: /mcp:stop <server-name>")
			}
			if b == nil {
				return ext.Errorf("no servers configured")
			}
			name := parts[1]
			if err := b.stopServer(name); err != nil {
				return ext.Errorf("stop %s: %v", name, err)
			}
			return ext.Display("stopped server: " + name)

		case "restart":
			if b == nil {
				return ext.Errorf("no servers configured")
			}
			b.stopAll()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := b.discoverAndRegister(ctx); err != nil {
				return ext.Errorf("restart failed: %v", err)
			}
			return ext.Display(formatStatusSummary(b))

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
}

// formatStatusSummary builds a human-readable status line.
func formatStatusSummary(b *bridge) string {
	lines := b.serverStatus()
	if len(lines) == 0 {
		return "mcp-bridge: no servers"
	}
	return "mcp-bridge: " + strings.Join(lines, " | ")
}
