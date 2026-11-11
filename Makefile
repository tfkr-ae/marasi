BIN     := marasi
PKG     := ./cmd/marasi
DIST    := dist
GO      ?= go
# A parent go.work can exclude this module; build and test it on its own.
export GOWORK ?= off
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
TARGETS := darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64
VERSION ?= $(shell date -u '+%d.%m.%Y (dev)')
# CHANNEL (release or nightly) and SIGNING_PUBLIC_KEY (the base64 line of the
# Marasi minisign public key, docs/adr/2026-10-18-updates-are-signed-with-an-embedded-key.md) are set by the release and nightly
# workflows. Both stay empty for local builds, which are development builds.
CHANNEL ?=
SIGNING_PUBLIC_KEY ?=
LDFLAGS := -X 'main.version=$(VERSION)' -X 'main.buildChannel=$(CHANNEL)' -X 'main.signingPublicKey=$(SIGNING_PUBLIC_KEY)'
# Pinned so every machine and CI run the same checks; v0.8.2 is built for go 1.26.
# `go run pkg@version` keeps it out of go.mod.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.DEFAULT_GOAL := build

.PHONY: build all test lint check clean install uninstall help autobahn autobahn-baseline $(TARGETS)

# Autobahn WebSocket testsuite (Docker). See `make help`.
CASES   ?= *
EXCLUDE ?=
MODES   ?= fuzzingclient,fuzzingserver
PEERS   ?= strict,lenient
RUNS    ?= proxy,checkpoint-forward,checkpoint-edit,extension
AUTOBAHN_FLAGS = -cases '$(CASES)' -exclude '$(EXCLUDE)' -modes '$(MODES)' -peers '$(PEERS)' $(if $(VERBOSE),-verbose)

help:
	@printf '%s\n' \
		'make build           build for this machine into dist/marasi' \
		'make linux-amd64     build one target into dist/marasi-linux-amd64' \
		'make all             build every target' \
		'make test            run tests' \
		'make lint            check gofmt, run go vet and staticcheck' \
		'make check           run lint, then the short tests; run it before you commit' \
		'make autobahn        run the Autobahn suite through Marasi and diff it against the' \
		'                     committed direct baseline; exits non-zero on a deviation outside 9.x.' \
		'                     Narrow with CASES=3.*,7.9.* EXCLUDE=9.* MODES=fuzzingclient' \
		'                     PEERS=strict RUNS=proxy (runs: proxy checkpoint-forward' \
		'                     checkpoint-edit extension). Reports land in dist/autobahn/.' \
		'make autobahn-baseline  rerun the selected cases with no proxy and merge them' \
		'                     into internal/autobahn/baseline/' \
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

lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to format these files (run gofmt -w on them):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	$(GO) vet ./...
	$(GO) run $(STATICCHECK) ./...

check: lint
	$(GO) test -short ./...

autobahn: build
	$(GO) run ./internal/autobahn/cmd/autobahn $(AUTOBAHN_FLAGS) -runs '$(RUNS)'

autobahn-baseline:
	$(GO) run ./internal/autobahn/cmd/autobahn $(AUTOBAHN_FLAGS) -runs direct

install: build
	@mkdir -p $(BINDIR)
	install -m 0755 $(DIST)/$(BIN) $(BINDIR)/$(BIN)

uninstall:
	rm -f $(BINDIR)/$(BIN)

clean:
	rm -rf $(DIST)
