# mcp-bridge

Connect zot to [MCP (Model Context Protocol)](https://modelcontextprotocol.io) servers.

This extension reads MCP server configurations from standard locations (same format as Claude Desktop, Cursor, Cline, etc.) and bridges their tools into zot so the LLM can call them directly.

## Features

- **Standard config format** — same JSON as Claude Desktop, Cursor, Cline
- **On-demand tool discovery** — a fixed set of six small tools is always advertised; matching MCP schemas load when needed instead of bloating every model request
- **Resources and prompts** — `mcp__resources` lists/reads resources and templates, `mcp__prompts` lists/renders prompt templates
- **Live catalogue** — `mcp__call` and `mcp__describe` reach tools discovered after startup; `tools/list_changed` refreshes the cache automatically
- **Server notifications** — MCP logging at warning and above, and resource-update notifications, surface as zot notifications
- **Roots** — the zot working directory is announced as the project root
- **Smart lazy loading** — cached definitions register as deferred tools at startup, servers wake for refresh or tool calls, then auto-sleep after idle time
- **Auto-respawn** — calling a loaded tool on a sleeping server wakes it up automatically
- **Multi-transport** — stdio, streamable-http, and SSE transports
- **Multi-server** — connect to any number of MCP servers simultaneously
- **Tool namespacing** — tools appear as `mcp__<server>__<tool>` to avoid collisions
- **Tool annotations** — read-only, destructive, idempotent hints surfaced to LLM
- **Configurable timeouts** — per-server connect, request, and idle timeouts
- **Custom headers** — auth tokens and other headers for HTTP servers
- **Slash commands** — `/mcp` for help, `/mcp status` to inspect servers, `/mcp install` to configure servers, and start/stop/restart commands
- **Better error messages** — context-aware errors with actionable suggestions

## Interactive OAuth

For an HTTP server requiring browser authorization, run `/mcp auth <server>` to open the authorization URL in your default browser (`open` on macOS, `rundll32` on Windows, `xdg-open` elsewhere). The URL is also displayed as a manual fallback; clipboard contents are not changed. The bridge uses the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) `auth` package for metadata discovery (protected-resource and authorization-server metadata, with the 2025-03-26 fallback), public-client registration (a stored registration is reused; a fresh `/mcp auth` registers anew), PKCE, token refresh, and step-up scopes. Discovery deliberately allows private IPs, because VPN-hosted MCP servers are the common case here. A loopback callback listener checks state and expires after five minutes. Background discovery never launches a login flow.

The command returns immediately; the result arrives as a notification once the browser round-trip completes, and the server reconnects on its own. Tokens and client registration are stored per exact resource URL under `$ZOT_HOME/mcp-oauth/`, using atomic writes and mode 0600 files (0700 directory on Unix). These files contain credentials: do not share or commit them. On Windows, protect the state directory with account-specific ACLs.

`/mcp logout <server>` stops that connection and deletes its local credentials; it does not revoke the authorization grant at the provider. Servers sharing an exact URL share credentials. Browser authorization, re-authorization after a purged client registration and fresh registration were validated end-to-end against n8n's MCP OAuth server.

### Per-server OAuth configuration

`oauth` accepts `true`, `false`, or an object with `clientId`, `clientSecret`, `scope`, and `redirectUri`. Fields support environment expansion. Explicit `false` disables OAuth; omission retains the existing stored-credential and explicit `/mcp auth` behavior. Configured clients take precedence over saved registrations; incompatible saved tokens are not reused.

`redirectUri`, if supplied, must be an HTTP loopback IP URL with an explicit port, e.g. `http://127.0.0.1:33418/callback`. Remote callbacks are not supported. Omit it for an ephemeral loopback port. Keep client secrets in environment variables, not committed config files.

URL-only server definitions infer Streamable HTTP unless `transport` or `type` selects another transport. Millisecond timeouts retain exact precision. Disabled entries stay visible in status and never connect or require environment secrets.

## Environment variables

Like [Claude Code](https://code.claude.com/docs/en/mcp#environment-variable-expansion-in-mcp-json), the bridge expands `${VAR}`, `${VAR:-default}` and (zot-mcp style) `$env:VAR` in `command`, `args`, `cwd`, `env` values, `url`, and `headers` values. This is client configuration compatibility, not a requirement of the MCP protocol.

```json
{"mcpServers":{"api":{"transport":"streamable-http","url":"${API_BASE_URL:-https://api.example.com}/mcp","headers":{"Authorization":"Bearer ${API_KEY}"}}}}
```

Expansion uses the bridge process environment after global/project configurations are merged. Defaults apply to unset variables; an explicitly empty variable stays empty. Values are expanded once, without shell execution, bare `$VAR` expansion, nested defaults, or references to sibling `env` entries. Config files are not rewritten. A missing variable without a default disables that server and reports the server, field, and variable name, never the field value; other valid servers remain available.

## Quick Start

Requires [Go 1.25+](https://go.dev/dl/) on `PATH`. Built on the official `modelcontextprotocol/go-sdk` (v2.0.0+; v1.x used `mark3labs/mcp-go`). Upgrading from v1.x keeps OAuth credentials; tool schemas are re-cached once because go-sdk preserves them verbatim. Like the upstream examples, the extension runs from source via `go run .` — no architecture-specific binary to ship. To skip the compile step at startup, `go build -o mcp-bridge .` in the installed directory and set `"exec": "./mcp-bridge"` (drop `args`) in `extension.json`.

1. **Install the extension:**

   ```bash
   zot ext install https://git.miskam.xyz/mxm/mcp-bridge
   ```

   From a checkout of the monorepo instead: `cd extensions/mcp-bridge && make`.

2. **Start zot and configure servers with slash commands:**

   ```text
   /mcp install filesystem -- npx -y @modelcontextprotocol/server-filesystem "."
   /mcp install context7 -- npx -y @upstash/context7-mcp@latest
   /mcp install -t http grep https://mcp.grep.app/
   ```

   These commands write to `.zot/mcp.json` in the working directory by default. Use `--scope project` for shared `.mcp.json` configuration or `--scope user` for `$ZOT_HOME/mcp.json`. Stdio examples require Node.js/`npx` when the servers start; installation itself does not download, launch, or contact a server. See [Install a server](#install-a-server) for flags, quoting, and authentication.

3. **Run `/reload-ext`.** The extension loads the configuration and refreshes its tool cache in the background. When zot shows `MCP tool cache changed`, run `/reload-ext` once more. Future launches register the cached MCP tools immediately as deferred definitions. Use `/mcp status` to inspect server states; bare `/mcp` shows help.

The model initially sees six small bridge tools, including `mcp__search_tools`; server-specific tool schemas are deferred. The search tool searches cached MCP tool names and descriptions locally and activates up to eight relevant definitions by default, which the model can then call normally. This keeps large MCP installations compatible with providers that limit request or tool-schema size.

## Migrating to 3.0

**Breaking command change:** `/mcp setup` and its template commands have been removed. Use the generic `/mcp install [options] <name> <commandOrUrl> [args...]` instead; there is no template catalog or `add` subcommand.

For example, replace the old command:

```text
/mcp setup add grep
```

with:

```text
/mcp install --scope user -t http grep https://mcp.grep.app/
/reload-ext
```

The explicit `--scope user` preserves the old setup command's global destination. The new install command defaults to **local** (`.zot/mcp.json`); `--scope project` writes `.mcp.json`. Supply the executable or URL explicitly when replacing other template commands, and put subprocess flags after `--`.

**Already configured servers need no reinstall.** Existing configuration and OAuth credentials are unchanged by the upgrade. Do not rerun install for existing entries: duplicate names in the target file are rejected. Browser authorization still uses `/mcp auth <name>`; OAuth install flags are not supported.

See [CHANGELOG.md](CHANGELOG.md) for the 3.0.0 release notes.

## Configuration

Config files are merged in this order; a later file replaces a same-named server entirely:

| Location | Scope |
|---|---|
| `$XDG_CONFIG_HOME/mcp/mcp.json` (default `~/.config/mcp/mcp.json`) | Shared across MCP clients (cross-client convention, all platforms) |
| `~/.agents/mcp.json`, `~/.agents/mcp/mcp.json` | Shared across agents |
| `$ZOT_HOME/mcp.json` | Global, platform-native (`~/Library/Application Support/zot` on macOS, `$XDG_STATE_HOME/zot` when set) |
| `.mcp.json` | Project-level (Claude Code compatible) |
| `.zot/mcp.json` | Project-level, zot-specific |

### Config Format

Standard MCP config — same as Claude Desktop and Claude Code, with a few optional extensions:

```jsonc
{
  "mcpServers": {
    // stdio: local subprocess
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."],
      "env": { "NODE_ENV": "production" },
      "cwd": "~/projects",                  // optional; default: zot's working directory
      "idleTimeout": 300                    // seconds before an unused server is stopped
    },

    // streamable-http with a static token ("type": "http" is the Claude Code alias)
    "atlassian": {
      "type": "http",
      "url": "https://mcp.example.com/mcp/",
      "headers": { "Authorization": "Bearer ${ATLASSIAN_TOKEN}" }
    },

    // streamable-http with browser OAuth: no headers, run /mcp auth n8n once
    "n8n": {
      "transport": "streamable-http",
      "url": "https://n8n.example.com/mcp-server/http",
      "requestTimeout": 120
    },

    // legacy SSE
    "legacy": {
      "transport": "sse",
      "url": "https://example.com/sse"
    },

    // kept in the file, never started
    "experimental": {
      "command": "node", "args": ["server.js"], "disabled": true
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
| `cwd` | string | project dir | Working directory for the subprocess; `~` and relative paths supported |
| `disabled` | bool | false | Keep the entry but never start the server |
| `transport` | string | "stdio" | "stdio", "streamable-http", or "sse" |
| `type` | string | — | Claude Code alias: "stdio", "http" (= streamable-http), or "sse" |
| `url` | string | — | Server URL (HTTP transports only) |
| `headers` | object | — | Static HTTP headers (HTTP transports only). Omit for OAuth servers and use `/mcp auth` |
| `connectTimeout` | number | 30 | Connection timeout in seconds |
| `requestTimeout` | number | 60 | Per-request timeout in seconds |
| `idleTimeout` | number | 300 | Idle timeout before stopping in seconds |
| `connectTimeoutMs`, `requestTimeoutMs` | number | — | Millisecond aliases (zot-mcp compatible); take precedence |

### You.com

Register the keyless You.com MCP profile (`you-search`), with no account or API key:

```text
/mcp install --scope user --transport http you "https://api.you.com/mcp?profile=free"
```

To use the authenticated server and its additional tools, edit
`$ZOT_HOME/mcp.json` after installing the configuration:

```jsonc
{
  "mcpServers": {
    "you": {
      "transport": "streamable-http",
      "url": "https://api.you.com/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_YDC_API_KEY"
      }
    }
  }
}
```

The authenticated endpoint does not expose `you-finance` by default. Request
it explicitly with the `?tools=` URL parameter or the `X-Allowed-Tools` header.

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

1. **Startup**: mcp-bridge reads config and registers tools from `mcp-tools-cache.json`
2. **Background refresh**: starts configured MCP servers, calls `tools/list`, and updates the cache when definitions change
3. **Reload**: if the cache changed, run `/reload-ext` once so zot rebuilds the tool registry with the new definitions
4. **Naming**: tools appear as `mcp__<server>__<tool>` (e.g., `mcp__filesystem__read_file`)
5. **Idle timeout**: servers not used for 5 minutes are automatically stopped
6. **Auto-respawn**: calling a tool on a stopped server wakes it up
7. **Routing**: tool calls are forwarded to the appropriate MCP server

## Slash Commands

| Command | Description |
|---|---|
| `/mcp` | Command reference |
| `/mcp status` | Status of all configured servers (last known, not a live check) |
| `/mcp status <name>` | Details and recent lifecycle log for one server |
| `/mcp start <name\|all>` | Start one server, or all |
| `/mcp stop <name\|all>` | Stop one server, or all |
| `/mcp restart` | Restart all servers |
| `/mcp refresh` | Rediscover tools and update the cache |
| `/mcp auth <name>` | Browser OAuth authorization (alias: `login`) |
| `/mcp logout <name>` | Remove local OAuth credentials |
| `/mcp install --help` | Installation options and examples |
| `/mcp install [options] <name> <commandOrUrl> [args...]` | Configure any stdio, HTTP, or SSE MCP server |
| `/mcp help` | Command reference |

### Install a server

`/mcp install` uses the core `claude mcp add` argument model: supply a name and
any executable or server URL. There is no built-in server catalog or template
requirement, and no `setup` or `add` subcommand.

```text
/mcp install --transport http grep https://mcp.grep.app/
/mcp install --transport sse events https://example.com/sse
/mcp install docs -- npx -y @upstash/context7-mcp@latest
/mcp install filesystem --scope project -- npx -y @modelcontextprotocol/server-filesystem "/path with spaces"
/mcp install worker -e API_KEY=${API_KEY} -- npx my-mcp-server --some-flag
/mcp install -t http api https://example.com/mcp -H "Authorization: Bearer ${API_TOKEN}"
```

| Option | Meaning |
|---|---|
| `-t`, `--transport <stdio\|http\|sse>` | Defaults to `stdio`; `http` maps to streamable HTTP (`streamable-http` is also accepted) |
| `-e`, `--env KEY=value` | Stdio environment variable; repeat the flag for multiple variables |
| `-H`, `--header "Name: value"` | HTTP/SSE header; repeat the flag for multiple headers |
| `-s`, `--scope <local\|project\|user>` | Defaults to `local`; see paths below |
| `--global`, `--project` | Aliases for `--scope user` and `--scope project` |
| `--` | Stop parsing install options; pass the remaining executable/arguments verbatim |
| `-h`, `--help` | Show help (also shown by bare `/mcp install`) |

Options may appear before or after the name/target, but must precede `--`.
Use `--` before the executable when it takes flags, so they are not interpreted
as install options. Long options also accept `--option=value`.
Single/double quotes and backslash escapes preserve spaces and empty arguments;
no shell, glob expansion, variable expansion, or command substitution runs during
installation. `${VAR}` / `${VAR:-default}` references are saved literally and
expanded by the bridge when it loads configuration. Prefer references to literal
secrets, especially in shared files or chat history.

**Scopes use zot's existing configuration locations**, not Claude's private store:

| Scope | File |
|---|---|
| `local` (default) | `<cwd>/.zot/mcp.json` |
| `project` | `<cwd>/.mcp.json` |
| `user` | `$ZOT_HOME/mcp.json` |

`local` means zot-specific, **not automatically private or ignored by Git**.
When names overlap, `.zot/mcp.json` takes precedence over `.mcp.json`, which takes
precedence over the user file. Duplicate names in the target file are rejected;
other server entries and unknown JSON fields (including top-level fields) are
preserved. Config files are written atomically with private, owner-only permissions
(mode 0600 on Unix).

This installs **configuration**, not a downloaded executable. The install command
starts no processes and makes no network requests. Run `/reload-ext` after installing. HTTP URLs need an explicit
HTTP/SSE transport; stdio environment flags cannot be combined with HTTP headers.
For browser OAuth use `/mcp auth <name>` after reload. Claude's OAuth installation
flags (`--client-id`, `--client-secret`, `--callback-port`) are not implemented by
this command; advanced OAuth settings remain available through `mcp.json`.
The former `/mcp setup` command and preset-only install syntax have been removed.

## Tool Exposure

The model always sees six fixed tools; everything else is deferred and loaded on demand.

| Tool | Purpose |
|---|---|
| `mcp__search_tools {query, limit}` | Search cached MCP tool names/descriptions and activate the matching deferred definitions |
| `mcp__call {server, tool, args}` | Call any tool by server and MCP tool name, including tools discovered after startup |
| `mcp__describe {server, tool?}` | Live tool list of a server, or one tool's input/output schema and annotations |
| `mcp__resources {server, action: list\|read\|subscribe\|unsubscribe, uri?}` | Resources and templates; text/images natively, other binary content as base64 JSON |
| `mcp__prompts {server, action: list\|get, name?, args?}` | Prompt templates and rendered messages |
| `mcp__control {server, action: ping\|logging/set\|complete, ...}` | Health check, logging level, prompt/resource argument completion |

Completion takes `ref` (`type: ref/prompt` with `name`, or `type: ref/resource` with `uri`), `argument: {name, value}`, and optional `context: {arguments: {...}}`. Logging takes `level` from `debug` through `emergency`. Tool-call progress is reported through notifications with a request token.

Subscriptions keep the connection awake. Explicit stop, logout, disconnect or extension reload ends subscriptions; subscribe again after reconnecting. Resource/prompt list-change notifications announce changes; listing always fetches the live catalogue.

Deferred tools are namespaced `mcp__<server>__<tool>` (non-alphanumerics become `_`), e.g. `mcp__filesystem__read_file`. Once activated by `mcp__search_tools` they are called natively with their real schema, which is why the bridge keeps them alongside the generic `mcp__call`.

## Smart Lazy Loading

The bridge uses a "smart lazy" strategy:

1. **On startup**: cached tool definitions are registered without blocking zot startup
2. **In the background**: servers start long enough to refresh the tool cache
3. **During use**: servers stay running for fast tool calls
4. **After 5 min idle**: unused servers are automatically stopped (saves memory/CPU)
5. **On next tool call**: the server is respawned automatically (~1-3s delay)

This gives you:
- Cached tools available for local search immediately, with schemas loaded on demand
- Fast tool calls when actively working
- Memory freed when not using MCP tools
- One manual `/reload-ext` only when tool definitions change

## Troubleshooting

**Check server status:**
```
/mcp status
```

**View extension logs:**
```bash
zot ext logs mcp-bridge -f
```

**Common issues:**

- **Server fails to start**: check that `command` exists in your PATH, or use absolute path
- **Tool not found**: run `/mcp status` to see if the server started successfully
- **Slow first call**: server is respawning after idle timeout (normal)

## Limitations

- **OAuth scope** — `/mcp auth <server>` supports HTTPS servers with dynamic public-client registration. Remote/headless callback forwarding is not supported. Static header authentication remains available.
- **No sampling/elicitation** — zot's extension protocol has no host API for nested model requests or user dialogs, so these server-to-client requests are not advertised
- **Parity validation in progress** — SSE authorization retry has a local HTTP test, not a full live SSE/browser acceptance run. Subscription continuity across reconnects is not implemented.
- **No automatic config hot reload** — run `/reload-ext` after install/config changes

## Binary releases

Forgejo Actions (`.forgejo/workflows/release.yml`) runs on new `v*` tags. GoReleaser v2.18.2 cross-compiles with `CGO_ENABLED=0` on a Linux runner:

- `mcp-bridge_<version>_linux_amd64.tar.gz` — Linux x86_64
- `mcp-bridge_<version>_linux_arm64.tar.gz` — Linux aarch64
- `mcp-bridge_<version>_darwin_arm64.tar.gz` — Apple Silicon macOS (unsigned, not notarized)
- `checksums.txt` — SHA-256 hashes

Each archive contains the executable, README, changelog, MIT license and an `extension.json` pointing at `./mcp-bridge`. The git manifest still uses `go run .`, so source installations remain compatible with current zot. The proposed [`binary` manifest block](https://github.com/patriceckhart/zot/discussions/183) is not enabled until zot supports downloading and verifying it.

Download your archive and `checksums.txt` from the same release. In a new working directory, verify before extracting (replace the filename below with the downloaded asset):

```sh
asset=mcp-bridge_3.0.0_darwin_arm64.tar.gz
# Select exactly this asset from the checksum file; fail if absent or duplicated.
awk -v asset="$asset" '$2 == asset { print; n++ } END { if (n != 1) exit 1 }' checksums.txt > selected-checksum.txt &&
shasum -a 256 -c selected-checksum.txt &&
mkdir extracted && tar -xzf "$asset" -C extracted &&
zot ext install ./extracted
```

Use `sha256sum -c` instead of `shasum -a 256 -c` on Linux if needed. Checksums detect corruption; they are not an independent release signature. Do not extract over your source checkout or an existing installation.

CI needs an `ubuntu-24.04` Forgejo runner with Node.js for JavaScript actions and network access to download Go and GoReleaser. The job token is passed as `GITEA_TOKEN` to publish to this repository's releases; repository write permission must be enabled. No macOS runner or Go installation is needed on the user's machine for binary installations.

Local packaging check (does not publish):

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish
```

Snapshots have a `-next` artifact name but keep the source version inside the binary and manifest. For releases, CI checks the tag against the manifest, and the existing version test checks the manifest against the code. Push a new version tag after merging the workflow; existing tags do not trigger it retroactively.

## Development

Source of truth: `https://git.miskam.xyz/mxm/mcp-bridge`. The monorepo `zot-extension` consumes it as a submodule at `extensions/mcp-bridge`. Work in a checkout outside `$ZOT_HOME/extensions/`; `make install` refuses to run from the installed copy or with unpushed commits, because `zot ext remove` deletes that directory.

```bash
go vet ./... && go test ./...        # unit tests
zot --ext .                          # run one zot session against this checkout
make install                         # zot ext remove + zot ext install .
zot ext logs mcp-bridge -f           # extension stderr
```

`sdkcompare/` is a separate module that drives the official go-sdk and the former mcp-go dependency through one interface; see its README for the migration acceptance results.

Release: bump `version.go` and `extension.json` (a test enforces they match), commit, `git tag -a v<version>`, `git push --tags`.

Validated against: `@modelcontextprotocol/server-filesystem` (stdio), `@zereight/mcp-gitlab` (stdio), grep.app (streamable-http), Atlassian MCP behind static headers (streamable-http), n8n MCP with OAuth 2.1 (streamable-http).

## License

MIT
