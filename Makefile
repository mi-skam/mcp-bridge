.PHONY: default build install uninstall

BIN := $(notdir $(CURDIR))
# Must match "name" in extension.json (slash command/logical extension name).
EXT := mcp
ZOT_HOME ?= $(shell \
	if [ -n "$$XDG_STATE_HOME" ]; then \
		printf '%s/zot' "$$XDG_STATE_HOME"; \
	elif [ "$$(uname -s)" = "Darwin" ]; then \
		printf '%s/Library/Application Support/zot' "$$HOME"; \
	else \
		printf '%s/.local/state/zot' "$$HOME"; \
	fi)

default: install

build:
	go build -o $(BIN) .

install:
	-zot ext remove $(EXT) --yes
	rm -rf "$(ZOT_HOME)/extensions/$(BIN)"
	mkdir -p "$(ZOT_HOME)/extensions/$(EXT)"
	cp extension.json "$(ZOT_HOME)/extensions/$(EXT)/extension.json"
	go build -o "$(ZOT_HOME)/extensions/$(EXT)/$(BIN)" .

uninstall:
	-zot ext remove $(EXT) --yes
