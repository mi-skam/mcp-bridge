.PHONY: default build install uninstall

BIN := $(notdir $(CURDIR))
EXT := $(notdir $(CURDIR))

default: install;

build:
	go build -o $(BIN) .

install: build
	-zot ext remove $(EXT) --yes
	zot ext install ./

uninstall:
	zot ext remove $(EXT)
