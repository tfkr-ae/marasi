BIN     := marasi
PKG     := ./cmd/marasi
DIST    := dist
GO      ?= go
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
TARGETS := darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64
VERSION ?= $(shell date -u '+%d.%m.%Y (dev)')
LDFLAGS := -X 'main.version=$(VERSION)'

.DEFAULT_GOAL := build

.PHONY: build all test clean install uninstall help $(TARGETS)

help:
	@printf '%s\n' \
		'make build           build for this machine into dist/marasi' \
		'make linux-amd64     build one target into dist/marasi-linux-amd64' \
		'make all             build every target' \
		'make test            run tests' \
		'make install         build and install to $(BINDIR)/$(BIN)' \
		'make uninstall       remove $(BINDIR)/$(BIN)' \
		'make clean           remove dist/'

build:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN) $(PKG)

$(TARGETS):
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$@)) GOARCH=$(word 2,$(subst -, ,$@)) \
		$(GO) build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-$@$(if $(findstring windows,$@),.exe) $(PKG)

all: $(TARGETS)

test:
	$(GO) test ./...

install: build
	@mkdir -p $(BINDIR)
	install -m 0755 $(DIST)/$(BIN) $(BINDIR)/$(BIN)

uninstall:
	rm -f $(BINDIR)/$(BIN)

clean:
	rm -rf $(DIST)
