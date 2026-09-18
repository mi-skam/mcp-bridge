# mcp-bridge

Connect zot to [MCP (Model Context Protocol)](https://modelcontextprotocol.io) servers so you can use their tools through natural-language requests. Supports local **stdio**, **Streamable HTTP**, and legacy **SSE** servers, using standard `mcpServers` JSON configuration.

The bridge also exposes resources, prompt templates, completion, logging, and subscriptions. Six small bridge tools are advertised eagerly; server-specific definitions load on demand instead of filling every model request. Servers refresh their tool cache in the background, sleep after idle time, and wake on tool calls.

## Install or update the extension

### From source

Requires [Go 1.25.5+](https://go.dev/dl/) on zot's `PATH`. The Git installation runs `go run .`:

```sh
zot ext install https://git.miskam.xyz/mxm/mcp-bridge
```

Start zot, or run `/reload-ext` in an existing session. Installing this extension is separate from configuring an MCP server with `/mcp install`.

**To update:** there is no `zot ext update` command. Back up any local edits in the installed extension directory first: removal deletes that directory. Close other zot sessions using it, and run these commands **from outside the installed directory**:

```sh
cd ~
zot ext remove mcp-bridge --yes
zot ext install https://git.miskam.xyz/mxm/mcp-bridge
```

Then restart zot or run `/reload-ext`. MCP configuration and OAuth credentials outside the extension directory remain intact. For a prebuilt update, use the same removal step, then install the newly verified extracted directory instead of the Git URL.

### From a v4.0.0 release archive (no Go required)

Download the matching archive and `checksums.txt` from the [v4.0.0 release](https://git.miskam.xyz/mxm/mcp-bridge/releases/tag/v4.0.0):

| Platform | Archive |
|---|---|
| Linux x86_64 | `mcp-bridge_4.0.0_linux_amd64.tar.gz` |
| Linux aarch64 | `mcp-bridge_4.0.0_linux_arm64.tar.gz` |
| macOS Apple Silicon | `mcp-bridge_4.0.0_darwin_arm64.tar.gz` |

In a fresh working directory containing both downloads, verify **before extracting** (change `asset` for your platform):

```sh
asset=mcp-bridge_4.0.0_darwin_arm64.tar.gz
# Require exactly one checksum entry for this archive.
awk -v asset="$asset" '$2 == asset { print; n++ } END { if (n != 1) exit 1 }' checksums.txt > selected-checksum.txt &&
shasum -a 256 -c selected-checksum.txt &&
mkdir extracted && tar -xzf "$asset" -C extracted &&
zot ext install ./extracted
```

On Linux, `sha256sum -c` can replace `shasum -a 256 -c`. Never extract over a checkout or existing installation. Each archive includes a binary and an `extension.json` that runs it; Git installs still use Go source. Checksums detect corruption, not an independent release signature. macOS binaries are **unsigned and not notarized**. Reload zot after installation.

## Quick start: configure, use, remove

In zot, configure a remote server (no Node.js needed for this example):

```text
/mcp install -t http grep https://mcp.grep.app/
/reload-ext
```

The bridge refreshes tools in the background. **Only if you see `MCP tool cache changed`, run `/reload-ext` once more.** Then inspect the connection:

```text
/mcp list
/mcp status grep
```

Ask zot in natural language, for example: **“Use grep's MCP tools to find Go examples of context.WithTimeout.”** The model searches and activates matching tool definitions, then calls the server. For a server requiring browser authorization, run `/mcp auth <name>` first.

When finished, remove the configuration and apply the change:

```text
/mcp uninstall grep
/reload-ext
```

Installation writes configuration only; it does not download packages, start processes, or contact the server. Those connections happen after reload. Removal does not immediately stop a running server; see [Uninstall a server](#uninstall-a-server) for credentials and scope behavior.

## Command cheatsheet

| Command | Task |
|---|---|
| `/mcp` or `/mcp help` | Show command help |
| `/mcp install` | Show install usage, options, and examples |
| `/mcp uninstall` | Show uninstall usage and scope behavior |
| `/mcp list` | Show all configured server states; takes no arguments |
| `/mcp status <name>` | Show one server's details and recent lifecycle log; exactly one name required |
| `/mcp start <name\|all>` | Start one server or all servers |
| `/mcp stop <name\|all>` | Stop one server or all servers; later tool calls can wake them |
| `/mcp restart` | Stop and restart all servers |
| `/mcp refresh` | Rediscover tools and update the cache; alias: `discover` |
| `/mcp auth <name>` | Start browser OAuth; alias: `login` |
| `/mcp logout <name>` | Stop the connection and delete local OAuth credentials, not the provider grant |
| `/mcp install [options] <name> <commandOrUrl> [args...]` | Add server configuration |
| `/mcp uninstall [options] <name>` | Remove configuration from one scope |

Status is **last known state**, not a live health check. `/mcp <name>` is a shortcut for server details; use `/mcp status list` for a server named `list`. Refresh is asynchronous: reload only when notified that the cache changed. There is no automatic configuration hot reload.

## Install a server

Supply any executable or URL; there is no server catalog or template requirement:

```text
/mcp install -t http api https://example.com/mcp -H "Authorization: Bearer ${API_TOKEN}"
/mcp install -t sse events https://example.com/sse
/mcp install docs -- npx -y @upstash/context7-mcp@latest
/mcp install --scope project filesystem -- npx -y @modelcontextprotocol/server-filesystem "/path with spaces"
/mcp install worker -e 'API_KEY=${API_KEY}' -- npx my-mcp-server --some-flag
/reload-ext
```

The `npx` examples require Node.js/`npx` when the server starts. Only run server executables you trust.

| Option | Meaning |
|---|---|
| `-t`, `--transport <stdio\|http\|sse>` | Default: `stdio`; `http` and `streamable-http` both select Streamable HTTP |
| `-e`, `--env KEY=value` | Repeatable stdio environment variable |
| `-H`, `--header "Name: value"` | Repeatable HTTP/SSE header |
| `-s`, `--scope <local\|project\|user>` | Target configuration file; default: `local` |
| `--global`, `--project` | Aliases for `--scope user` and `--scope project` |
| `--` | Stop option parsing; pass remaining executable/arguments verbatim |

Options may precede or follow the name/target, but must come before `--`. Long options accept `--option=value`. Quote values containing spaces; single/double quotes and backslash escapes preserve spaces and empty arguments. Put subprocess flags after `--` so they are not parsed as install options.

Installation performs **no shell execution, globbing, variable expansion, or command substitution**. Environment references are saved literally and expanded when configuration loads. Prefer references over literal secrets in configuration and chat history. HTTP URLs require explicit `-t http` or `-t sse`; `--env` is stdio-only and `--header` is HTTP/SSE-only.

The argument model resembles `claude mcp add`, but does **not** provide full Claude flag parity. OAuth install flags such as `--client-id`, `--client-secret`, and `--callback-port` are not supported; configure OAuth in JSON and use `/mcp auth` after reload.

### Choose a scope

| Scope | File |
|---|---|
| `local` (default) | `<cwd>/.zot/mcp.json` |
| `project` | `<cwd>/.mcp.json` |
| `user` | `$ZOT_HOME/mcp.json` |

`local` is zot-specific, **not automatically private or Git-ignored**. Duplicate names in the target file are rejected. Install and uninstall preserve other servers and unknown JSON fields, including top-level fields, and write atomically with owner-only permissions (0600 on Unix).

Configuration loads in this order; each later file replaces an entire same-named server, not individual fields:

1. `$XDG_CONFIG_HOME/mcp/mcp.json` (default `~/.config/mcp/mcp.json`)
2. `~/.agents/mcp.json`
3. `~/.agents/mcp/mcp.json`
4. `$ZOT_HOME/mcp.json`
5. `<cwd>/.mcp.json`
6. `<cwd>/.zot/mcp.json`

`$ZOT_HOME` means zot's state directory: the explicit environment override, otherwise `$XDG_STATE_HOME/zot` if set, then the platform default (`~/Library/Application Support/zot` on macOS, `~/.local/state/zot` on Linux, `%APPDATA%/zot` on Windows).

## Uninstall a server

```text
/mcp uninstall --scope project filesystem
/reload-ext
```

Uninstall accepts **exactly one name** and the same scope flags/aliases as install; default is `local`. Flags may appear before or after the name; `--scope=value` works, and `--` protects a name beginning with a dash.

- Only the named entry in the selected file is removed. A missing entry is an error; other scopes are never searched or deleted automatically.
- Other fields remain; removing the last entry leaves an empty `mcpServers` object.
- Running servers and registered tools remain unchanged until `/reload-ext`. Use `/mcp stop <name>` first if you need to stop the connection immediately.
- A same-name entry in a lower-precedence scope may become active after reload. Check all relevant files if the server reappears.
- OAuth credentials, cached tool definitions, executables, and packages are kept. To clear credentials too, run `/mcp logout <name>` **before uninstalling**; servers sharing an exact URL share credentials.

## Edit configuration and secrets

Files use standard JSON with a top-level `mcpServers` object. Edit the appropriate scope file, then run `/reload-ext`:

```json
{
  "mcpServers": {
    "api": {
      "type": "http",
      "url": "${API_BASE_URL:-https://api.example.com}/mcp",
      "headers": { "Authorization": "Bearer ${API_TOKEN}" },
      "requestTimeout": 120
    }
  }
}
```

| Fields | Behavior |
|---|---|
| `command`, `args`, `env`, `cwd` | Stdio process settings; `cwd` defaults to zot's working directory, supports `~` and relative paths |
| `transport`, `type`, `url`, `headers` | `transport` takes precedence; `type: "http"` aliases Streamable HTTP; URL-only entries infer it |
| `connectTimeout`, `requestTimeout`, `idleTimeout` | Seconds; defaults: 30, 60, 300 |
| `connectTimeoutMs`, `requestTimeoutMs` | Positive millisecond aliases take precedence with exact precision |
| `disabled` | `true` keeps the entry visible but never connects or requires environment secrets |
| `oauth` | Boolean or OAuth settings object; see below |

### Environment expansion

`${VAR}`, `${VAR:-default}`, and `$env:VAR` expand in `command`, `args`, `cwd`, `env` values, `url`, `headers` values, and OAuth string fields. Expansion uses the **bridge process environment after config merging**, not sibling `env` entries. Ensure zot inherits the variables when launched.

Defaults apply only to unset variables; explicitly empty variables stay empty. Expansion runs once, without shell execution, bare `$VAR`, or nested defaults; files are not rewritten. A missing variable without a default prevents that server from loading while other valid servers remain available. Errors identify the server, field, and variable name, never the secret value.

### Browser OAuth

Run `/mcp auth <name>` for an HTTP server that requires authorization. It opens your browser and displays a fallback URL without changing the clipboard. The command returns immediately; completion arrives as a notification and reconnects the server. Background discovery **never** starts an interactive login.

The official MCP Go SDK handles metadata discovery, public-client registration, PKCE, refresh, and step-up scopes. Saved registration is reused for normal connections; a fresh `/mcp auth` registers anew. Discovery allows private IPs for VPN-hosted servers, so use trusted endpoints. Authorization endpoints must use HTTPS. The loopback callback validates state and expires after five minutes.

`oauth` accepts `true`, `false`, or an object containing `clientId`, `clientSecret`, `scope`, and `redirectUri`. Explicit `false` disables OAuth; omission still allows saved credentials and explicit authorization. Configured clients override saved registrations; incompatible tokens are not reused. Keep client secrets in environment references.

A configured `redirectUri` must be an **HTTP loopback IP URL with an explicit port**, such as `http://127.0.0.1:33418/callback`; omit it for an ephemeral port. Remote/headless callback forwarding is unsupported. Static headers remain an alternative when the server supports them.

Credentials and registrations are stored per exact resource URL under `$ZOT_HOME/mcp-oauth/` with atomic writes (0600 files, 0700 directory on Unix). **Do not share or commit these files.** On Windows, protect the directory with account-specific ACLs. `/mcp logout` deletes local credentials but does not revoke the provider's authorization grant.

## Tool discovery and protocol support

With servers configured, the model sees these six eager tools:

| Tool | Purpose |
|---|---|
| `mcp__search_tools` | Search cached names/descriptions locally and activate matching definitions (default limit: eight) |
| `mcp__call` | Call by server/tool name, including tools discovered after startup |
| `mcp__describe` | Fetch a live tool list or one tool's input/output schemas and annotations |
| `mcp__resources` | List/read resources and templates; subscribe/unsubscribe |
| `mcp__prompts` | List prompt templates and render messages |
| `mcp__control` | Ping, set logging level, or complete prompt/resource arguments |

Cached server tools register at startup as **deferred definitions**, named `mcp__<server>__<tool>` (non-alphanumerics become `_`). Search activates their real schemas for native calls. New cache definitions require reload to join this registry; generic call/describe can reach the live catalog meanwhile. `tools/list_changed` refreshes the cache automatically.

The bridge announces zot's working directory as a root, forwards tool annotations and progress, surfaces warning-or-higher server logs and resource updates, and announces resource/prompt catalog changes. Resource text/images are native; other binary content is base64 JSON. Subscriptions keep connections awake, but stop, logout, disconnect, or reload ends them: subscribe again after reconnecting.

Limitations: no sampling or elicitation (zot has no host API for nested model requests/dialogs), no subscription continuity across reconnects, and no automatic config hot reload. Full live SSE/browser acceptance remains unvalidated. Idle servers normally stop after five minutes; the next tool call wakes them.

## Troubleshoot

| Symptom | Next action |
|---|---|
| `/mcp` is unavailable | Run `zot ext list`, then `zot ext doctor`; check Go on `PATH` for source installs; reload |
| Server missing after install | Run `/reload-ext`; check scope, working directory, and environment-expansion errors |
| Server fails to start | Inspect `/mcp status <name>` and `zot ext logs mcp-bridge -f`; check executable/`npx`, URL, network, and headers |
| Tools missing or outdated | Run `/mcp refresh`; reload once if notified that the cache changed; ask zot to search the MCP tools |
| Authorization required | Run `/mcp auth <name>`; open the displayed URL if the browser does not launch |
| Missing environment variable | Set it in zot's launch environment and restart zot; never paste the secret into chat |
| Install reports duplicate name | Edit the existing entry in the selected file and reload, or choose another name |
| Uninstall says not configured / server reappears | Select the correct `--scope`; inspect same-name entries in other config files |
| First call is slow | Idle wake-up is normal; inspect per-server timeouts if it fails rather than merely delays |
| Bare `/mcp status` shows usage | Use `/mcp list` for all servers or `/mcp status <name>` for one |

## Upgrade to v4.0.0

Replace bare `/mcp status` with **`/mcp list`**; `/mcp status <name>` requires exactly one name. Existing configured servers and OAuth credentials are unchanged—do not reinstall server entries. From pre-v3 versions, replace `/mcp setup` templates with generic `/mcp install`; use `--scope user` to retain the former global destination (the new default is local). See [CHANGELOG.md](CHANGELOG.md).

## Development

Source of truth: [mxm/mcp-bridge](https://git.miskam.xyz/mxm/mcp-bridge). `zot-extension` consumes it as the `extensions/mcp-bridge` submodule. Work in that source checkout, commit/publish changes there first, then update the parent repository's submodule pointer. Never develop inside `$ZOT_HOME/extensions/`.

```sh
go vet ./... && go test ./...
zot --ext .
```

`zot --ext .` shadows an installed extension of the same name for one session without copying it. `make install` removes/reinstalls from the checkout and refuses installed-directory use, uncommitted changes, or unpushed commits. To run compiled source instead, build with `go build -o mcp-bridge .`, set `extension.json`'s `exec` to `./mcp-bridge`, and remove its `args`.

## License

MIT
