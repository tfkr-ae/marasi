BIN     := marasi
PKG     := ./cmd/marasi
DIST    := dist
GO      ?= go
# A parent go.work can exclude this module; build and test it on its own.
ifeq ($(GOWORK),)
export GOWORK := off
endif
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
# Build tags. Internal only: TAGS=h1engine builds Marasi on its own HTTP/1.1
# engine instead of Martian, for `make conformance TAGS=h1engine` until the switch.
TAGS ?=

.DEFAULT_GOAL := build

.PHONY: build all test lint check clean install uninstall help conformance conformance-baseline conformance-update-known autobahn autobahn-baseline $(TARGETS)

# Conformance harness (Docker). See `make help`.
# The blocking HTTP/1.1 suites; the h2 suites and autobahn run on their own.
SUITES     ?= h1-origin,h1-client
CASES      ?= *
EXCLUDE    ?=
APPROACHES ?=
MODES      ?= fuzzingclient,fuzzingserver
PEERS      ?= strict,lenient
RUNS       ?= proxy,checkpoint-forward,checkpoint-edit,extension
PHASE      ?=
# The HTTP suites run Marasi in a Linux container, built for this architecture.
LINUX_ARCH ?= $(shell $(GO) env GOARCH)
CONFORMANCE      = $(GO) run ./internal/conformance/cmd/conformance
CONFORMANCE_FLAGS = -linux-arch '$(LINUX_ARCH)' -cases '$(CASES)' -exclude '$(EXCLUDE)' -approaches '$(APPROACHES)' -modes '$(MODES)' -peers '$(PEERS)' $(if $(VERBOSE),-verbose)

help:
	@printf '%s\n' \
		'make build           build for this machine into dist/marasi' \
		'make linux-amd64     build one target into dist/marasi-linux-amd64' \
		'make all             build every target' \
		'make test            run tests' \
		'make lint            check gofmt, run go vet and staticcheck' \
		'make check           run lint, then the short tests; run it before you commit' \
		'make conformance     conformance harness: run the suites (Docker) directly and' \
		'                     through Marasi, and score each case against its direct baseline' \
		'                     and deviations.json in internal/conformance/baselines/<suite>/.' \
		'                     Exits non-zero on a new deviation (listed in no entry), a known' \
		'                     deviation that went away (its cases ran and Marasi now matches' \
		'                     the baseline: remove the entry or run conformance-update-known)' \
		'                     or a stale entry: one that matched nothing although a run' \
		'                     and target it lists ran (with CASES, EXCLUDE or APPROACHES set,' \
		'                     only one that covers a case that ran). SUITES defaults to the' \
		'                     blocking h1-origin,h1-client. Suites: autobahn (Marasi on the' \
		'                     host), h1-origin, h1-client, h2-client and h2-origin (Marasi' \
		'                     in a Linux container built for LINUX_ARCH, this machine by' \
		'                     default; the h2 suites are report only, proxy run only: run' \
		'                     them on their own).' \
		'                     Narrow with SUITES=autobahn' \
		'                     RUNS=proxy CASES=3.*,7.9.* EXCLUDE=9.* APPROACHES=connect-tls,' \
		'                     and for autobahn MODES=fuzzingclient PEERS=strict (runs: proxy' \
		'                     checkpoint-forward checkpoint-edit extension). VERBOSE=1 streams' \
		'                     tester output. Reports land in dist/conformance/<suite>/<run>/.' \
		'                     Quick runs: SUITES=h1-origin RUNS=proxy,' \
		'                     SUITES=h1-client RUNS=proxy CASES=SMUG-CL-TE-BOTH@* (exact IDs run' \
		'                     only those probes), SUITES=h2-origin RUNS=proxy. TAGS=h1engine' \
		'                     runs Marasi on its own HTTP/1.1 engine instead of Martian. CI: the' \
		'                     conformance workflow (h1 jobs are required checks).' \
		'make conformance-baseline  rerun the selected cases with no proxy and merge them' \
		'                     into the direct baselines' \
		'make conformance-update-known PHASE=2  run, then rewrite the known entries of' \
		'                     deviations.json (adds new deviations with PHASE, narrows or removes' \
		'                     went-away ones) and score.json; accepted entries are never touched.' \
		'                     Review the diff before you commit it' \
		'make autobahn        make conformance SUITES=autobahn' \
		'make autobahn-baseline  make conformance-baseline SUITES=autobahn' \
		'make install         build and install to $(BINDIR)/$(BIN)' \
		'make uninstall       remove $(BINDIR)/$(BIN)' \
		'make clean           remove dist/'

build:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -tags '$(TAGS)' -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN) $(PKG)

$(TARGETS):
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$@)) GOARCH=$(word 2,$(subst -, ,$@)) \
		$(GO) build -tags '$(TAGS)' -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN)-$@$(if $(findstring windows,$@),.exe) $(PKG)

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

conformance: build linux-$(LINUX_ARCH)
	$(CONFORMANCE) $(CONFORMANCE_FLAGS) -suites '$(SUITES)' -runs '$(RUNS)'

conformance-baseline:
	$(CONFORMANCE) $(CONFORMANCE_FLAGS) -suites '$(SUITES)' -runs direct

conformance-update-known: build linux-$(LINUX_ARCH)
	$(CONFORMANCE) $(CONFORMANCE_FLAGS) -suites '$(SUITES)' -runs '$(RUNS)' -update-known $(if $(PHASE),-phase '$(PHASE)')

# The autobahn aliases are the conformance targets with SUITES=autobahn.
autobahn: SUITES = autobahn
autobahn: conformance

autobahn-baseline: SUITES = autobahn
autobahn-baseline: conformance-baseline

install: build
	@mkdir -p $(BINDIR)
	install -m 0755 $(DIST)/$(BIN) $(BINDIR)/$(BIN)

uninstall:
	rm -f $(BINDIR)/$(BIN)

clean:
	rm -rf $(DIST)
