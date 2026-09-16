.PHONY: default build install uninstall test

# Must match "name" in extension.json.
EXT := mcp

default: install

build:
	go build -o mcp-bridge .

test:
	go vet ./... && go test ./...

# Copies this directory (incl. run.sh, which builds on first start).
install:
	-zot ext remove $(EXT) --yes
	zot ext install .

uninstall:
	-zot ext remove $(EXT) --yes
