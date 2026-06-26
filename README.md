# mcp-bridge

Connect zot to [MCP (Model Context Protocol)](https://modelcontextprotocol.io) servers.

This extension reads MCP server configurations from standard locations (same format as Claude Desktop, Cursor, Cline, etc.) and bridges their tools into zot so the LLM can call them directly.

## Features

- **Standard config format** — same JSON as Claude Desktop, Cursor, Cline
- **Smart lazy loading** — servers spawn on startup to discover tools, then auto-sleep after idle time
- **Auto-respawn** — calling a tool on a sleeping server wakes it up automatically
- **Multi-transport** — stdio, streamable-http, and SSE transports
- **Multi-server** — connect to any number of MCP servers simultaneously
- **Tool namespacing** — tools appear as `mcp__<server>__<tool>` to avoid collisions
- **Tool annotations** — read-only, destructive, idempotent hints surfaced to LLM
- **Configurable timeouts** — per-server connect, request, and idle timeouts
- **Custom headers** — auth tokens and other headers for HTTP servers
- **Slash commands** — `/mcp` to check status, start/stop/restart servers
- **Better error messages** — context-aware errors with actionable suggestions

## Quick Start

1. **Build the extension:**

   ```bash
   cd examples/extensions/mcp-bridge
   go build -o mcp-bridge .
   ```

2. **Create a config file:**

   ```bash
   mkdir -p ~/.config/zot  # or ~/Library/Application\ Support/zot on macOS
   cat > ~/.config/zot/mcp.json << 'EOF'
   {
     "mcpServers": {
       "filesystem": {
         "command": "npx",
         "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
       },
       "context7": {
         "command": "npx",
         "args": ["-y", "@upstash/context7-mcp@latest"]
       }
     }
   }
   EOF
   ```

3. **Install the extension:**

   ```bash
   zot ext install ./mcp-bridge
   ```

4. **Restart zot** — your MCP tools are ready!

## Configuration

Config files are loaded from two locations (project overrides global per-server):

| Location | Scope |
|---|---|
| `$ZOT_HOME/mcp.json` | Global (macOS: `~/Library/Application Support/zot/mcp.json`) |
| `.zot/mcp.json` | Project-level (in your project root) |

### Config Format

Standard MCP config — same as Claude Desktop, with zot-specific extensions:

```jsonc
{
  "mcpServers": {
    // ── Stdio transport (local subprocess) ───────────────────────────────────
    "filesystem": {
      "command": "npx",                    // executable to spawn
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {                             // extra environment variables
        "NODE_ENV": "production"
      },
      "connectTimeout": 30,                // connection timeout (seconds)
      "requestTimeout": 60,                // per-request timeout (seconds)
      "idleTimeout": 300                   // idle timeout before stopping (seconds)
    },

    // ── Streamable HTTP transport (modern HTTP) ─────────────────────────────
    "supabase": {
      "transport": "streamable-http",
      "url": "https://mcp.supabase.com/mcp",
      "headers": {                         // custom HTTP headers
        "Authorization": "Bearer YOUR_TOKEN"
      }
    },

    // ── SSE transport (legacy HTTP) ─────────────────────────────────────────
    "legacy-server": {
      "transport": "sse",
      "url": "https://example.com/sse"
    }
  }
}
```

### Configuration Options

| Field | Type | Default | Description |
|---|---|---|---|
| `command` | string | — | Executable to spawn (stdio only) |
| `args` | string[] | [] | Arguments for the command |
| `env` | object | — | Extra environment variables |
| `transport` | string | "stdio" | Transport: "stdio", "streamable-http", or "sse" |
| `url` | string | — | Server URL (HTTP transports only) |
| `headers` | object | — | Custom HTTP headers (HTTP transports only) |
| `connectTimeout` | number | 30 | Connection timeout in seconds |
| `requestTimeout` | number | 60 | Per-request timeout in seconds |
| `idleTimeout` | number | 300 | Idle timeout before stopping in seconds |

### Example: Multiple Servers

```jsonc
{
  "mcpServers": {
    // Filesystem access (stdio)
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"]
    },

    // Grep.app - Search GitHub (streamable-http)
    "grep": {
      "transport": "streamable-http",
      "url": "https://mcp.grep.app/"
    },

    // Database queries (stdio)
    "sqlite": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-sqlite", "test.db"]
    },

    // Documentation lookup (stdio)
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp@latest"]
    }
  }
}
```

## How It Works

```
┌──────────────────────────────────────────────────────────────┐
│  zot agent                                                    │
│                                                               │
│  ┌──────────┐    tool_call    ┌──────────────┐               │
│  │   LLM    │───────────────▶│  mcp-bridge  │               │
│  │          │◀───────────────│  (extension) │               │
│  └──────────┘    tool_result └──────┬───────┘               │
│                                      │                        │
│                           ┌──────────┼──────────┐            │
│                           ▼          ▼          ▼            │
│                    ┌──────────┐ ┌──────────┐ ┌──────────┐   │
│                    │   MCP    │ │   MCP    │ │   MCP    │   │
│                    │ server 1 │ │ server 2 │ │ server 3 │   │
│                    │ (stdio)  │ │ (stdio)  │ │ (stdio)  │   │
│                    └──────────┘ └──────────┘ └──────────┘   │
└──────────────────────────────────────────────────────────────┘
```

1. **Startup**: mcp-bridge reads config, spawns all MCP servers
2. **Discovery**: calls `tools/list` on each server, registers tools with zot
3. **Naming**: tools appear as `mcp__<server>__<tool>` (e.g., `mcp__filesystem__read_file`)
4. **Idle timeout**: servers not used for 5 minutes are automatically stopped
5. **Auto-respawn**: calling a tool on a stopped server wakes it up
6. **Routing**: tool calls are forwarded to the appropriate MCP server

## Slash Commands

| Command | Description |
|---|---|
| `/mcp` | Show status of all configured servers |
| `/mcp <name>` | Show detailed status for one server |
| `/mcp:start <name>` | Manually start a server |
| `/mcp:stop <name>` | Manually stop a server |
| `/mcp:restart` | Restart all servers |

## Tool Naming

Tools are namespaced to avoid collisions with zot's built-in tools:

```
mcp__<server>__<tool>
```

Examples:
- `mcp__filesystem__read_file`
- `mcp__filesystem__write_file`
- `mcp__sqlite__query`
- `mcp__context7__resolve-library-id`

Server and tool names are sanitized (non-alphanumeric characters become `_`).

## Smart Lazy Loading

The bridge uses a "smart lazy" strategy:

1. **On startup**: all servers spawn, tools are discovered and registered
2. **During use**: servers stay running for fast tool calls
3. **After 5 min idle**: unused servers are automatically stopped (saves memory/CPU)
4. **On next tool call**: the server is respawned automatically (~1-3s delay)

This gives you:
- ✅ All tools visible to the LLM immediately
- ✅ Fast tool calls when actively working
- ✅ Memory freed when not using MCP tools
- ✅ No manual server management needed

## Troubleshooting

**Check server status:**
```
/mcp
```

**View extension logs:**
```bash
zot ext logs mcp-bridge -f
```

**Common issues:**

- **Server fails to start**: check that `command` exists in your PATH, or use absolute path
- **Tool not found**: run `/mcp` to see if the server started successfully
- **Slow first call**: server is respawning after idle timeout (normal)

## Limitations

- **No OAuth flow** — authentication requires manual token configuration in headers
- **No resources/prompts** — only tools are bridged (MCP resources and prompts coming later)
- **No config hot reload** — restart zot after config changes

## Development

```bash
# Build
cd examples/extensions/mcp-bridge
go build -o mcp-bridge .

# Run without installing (for one zot session)
zot --ext ./mcp-bridge

# View logs
zot ext logs mcp-bridge -f
```

## License

MIT

## Testing

### Tested Servers

✅ **Filesystem Server** (stdio)
```json
{
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/private/tmp"]
}
```

✅ **Grep.app** (streamable-http)
```json
{
  "transport": "streamable-http",
  "url": "https://mcp.grep.app/"
}
```
**Note:** The root endpoint `/` is required. Streamable HTTP protocol headers are handled automatically by the bridge.

### Test Results

- **Filesystem**: 14 tools registered, all working correctly
- **Grep.app**: 1 tool registered (`searchGitHub`), successfully searched GitHub repositories and returned 309 code snippets

