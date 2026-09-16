#!/bin/sh
# Launcher for `zot ext install <git-url>`: the clone has no binary, so build
# once on first start, then exec. Requires the Go toolchain on PATH.
# ponytail: unix only; swap for committed per-platform binaries when a
# user without Go shows up.
set -e
cd "$(dirname "$0")"
if [ ! -x mcp-bridge ] || [ -n "$(find . -name '*.go' -newer mcp-bridge 2>/dev/null | head -1)" ]; then
	command -v go >/dev/null 2>&1 || { echo "mcp-bridge: go toolchain not found; install Go 1.25+ or build ./mcp-bridge manually" >&2; exit 1; }
	go build -o mcp-bridge . 1>&2
fi
exec ./mcp-bridge "$@"
