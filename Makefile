.PHONY: default build install uninstall test

# Must match "name" in extension.json.
EXT := mcp

default: install

build:
	go build -o mcp-bridge .

test:
	go vet ./... && go test ./...

# Copies this directory; extension.json runs `go run .` so no binary is needed.
# Refuses to run from inside the installed copy: `zot ext remove` deletes that
# directory, which would destroy the source (and any unpushed commits) first.
install:
	@case "$(CURDIR)" in */zot/extensions/*) echo "refusing: run make install from a source checkout, not the installed extension dir" >&2; exit 1;; esac
	@test -z "$$(git status --porcelain)" || { echo "refusing: uncommitted changes" >&2; exit 1; }
	@git diff --quiet @{u} 2>/dev/null || { echo "refusing: unpushed commits; git push first" >&2; exit 1; }
	-zot ext remove $(EXT) --yes
	zot ext install .

uninstall:
	-zot ext remove $(EXT) --yes
