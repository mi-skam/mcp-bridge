# Changelog

## [4.0.0] - 2026-09-18

### Breaking changes

- `/mcp list` now shows the all-server overview previously displayed by bare `/mcp status`. `/mcp status <server>` requires exactly one server name; missing or extra arguments show usage. The existing `/mcp <server>` shortcut remains available; use `/mcp status list` for a server named `list`.

### Changed

- Help menus, documentation, and error hints now advertise bare `/mcp install` and `/mcp uninstall` for usage and examples, without requiring help flags.
- The README is organized around installing, using, inspecting, and removing servers, with a command reference, update instructions, and troubleshooting.

### Added

- `/mcp uninstall [options] <name>` removes one server configuration using the same local/project/user scopes as install. Other entries and unknown JSON fields are preserved; missing entries and malformed files are left untouched.
- Install and uninstall share scope resolution and serialized, atomic configuration writes. Removal requires `/reload-ext` to take effect and retains OAuth credentials, tool caches, and installed server packages.

### Migration

- Replace bare `/mcp status` with `/mcp list`; continue using `/mcp status <name>` for one server.
- Remove test or unused server entries with `/mcp uninstall <name>`, selecting `--scope project` or `--scope user` when appropriate, then run `/reload-ext`.
- Already configured servers and OAuth credentials need no migration or reinstall. Only the extension needs updating.

## [3.0.0] - 2026-09-18

### Breaking changes

- Removed `/mcp setup` and its template commands, including `/mcp setup templates` and `/mcp setup add`. Server installation is now generic: `/mcp install [options] <name> <commandOrUrl> [args...]`. Supply an executable or URL rather than a preset name.
- The install command defaults to `--scope local` (`<cwd>/.zot/mcp.json`). Use `--scope project` for `<cwd>/.mcp.json` or `--scope user` for `$ZOT_HOME/mcp.json`; scopes use zot's existing locations, not Claude's private configuration store.

### Added

- Core `claude mcp add`-style options: `-t`/`--transport` (`stdio`, `http`, `sse`), repeatable `-e`/`--env` for stdio, repeatable `-H`/`--header` for HTTP/SSE, and `-s`/`--scope`. `http` maps to Streamable HTTP; `--global` and `--project` remain scope aliases.
- Quoted values, backslash escapes, empty arguments, and `--` forwarding for subprocess arguments. Environment references are saved literally and resolved when configuration loads; installation performs no shell expansion or execution.
- Atomic, private configuration writes (mode 0600 on Unix), preserving other server entries and unknown JSON fields, including top-level fields. Duplicate names in the destination file are rejected rather than overwritten.
- Configuration-only installation: no downloads, subprocess launches, or network requests during `/mcp install`. Run `/reload-ext` afterward to load the configuration and discover tools.

### Command reference

- Bare `/mcp` shows help; `/mcp status` shows last-known server states, not a live health check.
- OAuth installation flags (`--client-id`, `--client-secret`, `--callback-port`) are not supported. Use `/mcp auth <name>` after reload for browser authorization, or configure advanced OAuth settings in `mcp.json`.

### Migration

Replace:

```text
/mcp setup add grep
```

with:

```text
/mcp install --scope user -t http grep https://mcp.grep.app/
/reload-ext
```

The explicit user scope retains the old setup command's global destination. For other former templates, provide the server command or URL explicitly; use `--` before executables with flags.

**Already configured servers need no reinstall.** Existing configuration files and OAuth credentials are unchanged by this upgrade. Do not run install again for entries already present in the target file.
