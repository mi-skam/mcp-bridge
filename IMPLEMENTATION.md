# MCP Bridge Implementation Summary

## What Was Built

A complete MCP (Model Context Protocol) bridge extension for zot that connects zot to any MCP server.

## Files Created

```
examples/extensions/mcp-bridge/
├── extension.json      # Extension manifest
├── go.mod              # Go module definition
├── go.sum              # Dependency checksums
├── config.go           # Config loading (global + project)
├── server.go           # MCP server lifecycle management
├── bridge.go           # Tool registration and routing
├── main.go             # Entry point
├── README.md           # Documentation
└── mcp-bridge          # Compiled binary
```

## Key Features Implemented

### 1. Standard Config Format
- Same JSON format as Claude Desktop, Cursor, Cline
- Global config: `~/Library/Application Support/zot/mcp.json` (macOS)
- Project config: `.zot/mcp.json`
- Project config overrides global per-server

### 2. Smart Lazy Loading
- Servers spawn on startup to discover tools
- Tools registered immediately (LLM sees them)
- Servers auto-sleep after 5 minutes of idle time
- Auto-respawn on next tool call (~1-3s delay)

### 3. Tool Namespacing
- Format: `mcp__<server>__<tool>`
- Avoids collisions with zot built-ins
- Sanitizes names (non-alphanumeric → `_`)

### 4. Slash Commands
- `/mcp` — show all server status
- `/mcp <name>` — detailed server status
- `/mcp:start <name>` — manually start server
- `/mcp:stop <name>` — manually stop server
- `/mcp:restart` — restart all servers

### 5. Multi-Server Support
- Concurrent server spawning
- Independent lifecycle per server
- Partial failure tolerance (one server failing doesn't break others)

## Test Results

✅ Successfully tested with `@modelcontextprotocol/server-filesystem`:
- Spawned server process
- Discovered 14 tools
- Registered all tools with zot
- Proper protocol frames sent (hello, register_tool, ready)

## Architecture

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

## Usage

1. **Build:**
   ```bash
   cd examples/extensions/mcp-bridge
   go build -o mcp-bridge .
   ```

2. **Configure:**
   ```bash
   mkdir -p ~/Library/Application\ Support/zot
   cat > ~/Library/Application\ Support/zot/mcp.json << 'EOF'
   {
     "mcpServers": {
       "filesystem": {
         "command": "npx",
         "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
       }
     }
   }
   EOF
   ```

3. **Install:**
   ```bash
   zot ext install ./mcp-bridge
   ```

4. **Use:**
   - Restart zot
   - Tools appear as `mcp__filesystem__read_file`, etc.
   - LLM can call them directly

## Dependencies

- `github.com/mark3labs/mcp-go v0.55.1` — MCP client library
- `github.com/patriceckhart/zot` — zot extension SDK

## Limitations

- **Stdio only** — HTTP transports (streamable-http, sse) not yet supported
- **No OAuth** — authentication not implemented
- **Tools only** — MCP resources and prompts not bridged yet

## Next Steps

1. Add HTTP transport support (streamable-http, sse)
2. Add OAuth authentication flow
3. Bridge MCP resources and prompts
4. Add tool annotations (readOnlyHint, destructiveHint, etc.)
5. Implement health checks and auto-reconnection
6. Add per-server timeout configuration

## Comparison to Other Harnesses

| Feature | Claude Desktop | Cursor | Pi | Zot MCP Bridge |
|---------|---------------|--------|----|----------------|
| Config format | ✅ Standard | ✅ Standard | ✅ Standard | ✅ Standard |
| Multi-server | ✅ | ✅ | ✅ | ✅ |
| Tool namespacing | ❌ | ❌ | ✅ | ✅ |
| Lazy loading | ❌ (all eager) | ❌ (all eager) | ✅ (manual) | ✅ (auto) |
| Idle timeout | ❌ | ❌ | ❌ | ✅ (5 min) |
| Auto-respawn | ❌ | ❌ | ❌ | ✅ |
| Slash commands | ❌ | ❌ | ✅ | ✅ |

**Zot's advantage**: Smart lazy loading gives the best of both worlds — tools visible immediately, but servers sleep when not in use.
