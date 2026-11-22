# Marasi

[![Go reference](https://pkg.go.dev/badge/github.com/tfkr-ae/marasi.svg)](https://pkg.go.dev/github.com/tfkr-ae/marasi)

<img src="images/logo.svg" width="150" alt="Marasi">

Marasi is an application security testing proxy for HTTP/1.1, HTTP/2, WebSockets, and streamed traffic (gRPC, gRPC-Web, Connect, SSE, and NDJSON). You drive it through the `marasi` CLI, store traffic and assessment work in SQLite `.marasi` projects, and extend it with Lua. It also works as a Go library.

[marasi.app](https://marasi.app)

## Features

- **HTTP/1.1 and HTTP/2 proxy**: Full request/response capture, metadata, notes, highlights, sitemap, and AIP-160 search (`traffic`, `notes`).
- **WebSockets and streaming**: Inspect, inject into, and close WebSockets (`websocket`), and read, inject into, or cancel streamed pairs over gRPC, gRPC-Web, Connect, SSE, and NDJSON (`stream`), with schema-less protobuf text decoding.
- **Checkpoint**: Hold, edit, forward, or drop in-flight HTTP requests, responses, WebSocket messages, stream messages, and trailers (`checkpoint`).
- **Launchpad**: Group captured pairs and launch edited raw HTTP/1.1, HTTP/2, or streamed requests (`launchpad`).
- **Armory**: Wordlist-driven request templates with payload positions (`armory`, `wordlist`).
- **Lua extensions**: Scriptable hooks for requests, responses, WebSockets, and stream messages (`extension`). New projects include `compass` (scope), `checkpoint` (interception rules), and `workshop` (Lua scratchpad).
- **Routing and Chrome**: Scope checks (`scope`), host:port overrides (`waypoint`), HTTP/HTTPS/SOCKS5 upstream proxies (`upstream`), and managed Chrome profiles with optional CDP (`chrome`).
- **Assessment and reporting**: Test cases, CVSS 3.1 findings, file artifacts, and template-based reports (`test-case`, `finding`, `artifact`, `report`).
- **SQLite projects**: Self-contained `.marasi` files holding traffic and assessment data.

## Install

Download a release binary or archive from [GitHub Releases](https://github.com/tfkr-ae/marasi/releases) (Linux `amd64`, macOS `arm64`, Windows `amd64`), or build from source with Go 1.26+:

```sh
make install   # installs to ~/.local/bin/marasi (override with PREFIX=...)
```

Use `make build` to compile `dist/marasi` without installing, or `go build -o marasi ./cmd/marasi`. Builds do not require CGO.

Release and nightly builds update in place through a running service instance:

```sh
marasi update
marasi update install
marasi service restart
```

Switch between `release` and `nightly` with `marasi update channel`.

## Quick start

Start a service instance, export its CA certificate, and proxy a request:

```sh
# Start an instance with a project and proxy listener
marasi service start --project-name assessment --address 127.0.0.1 --port 8080

# Export the CA certificate and send a request through the proxy
marasi certificate get > marasi-ca.pem
curl --proxy http://127.0.0.1:8080 --cacert marasi-ca.pem https://example.com

# List, search, and inspect captured traffic
marasi traffic list
marasi traffic list -q 'host = "example.com" AND status_code = 200'
marasi traffic get <pair-id>

# Stop the instance when done
marasi service stop
```

Run `marasi --help` or `marasi <command> --help` for all commands and flags. Pass `--json` to any command for JSON output.

To let a coding agent use Marasi, export the skill file matching your installed CLI:

```sh
marasi skill > ~/.agents/skills/marasi/SKILL.md
```

## Service instances and remote control

`marasi service start` launches a background service instance that owns one open `.marasi` project. Use `--instance NAME` to run multiple instances against different projects, `marasi project open` to switch projects without restarting the proxy listener, and `marasi listener stop` to pause capture while keeping the instance available for inspection.

Locally, the CLI talks to an instance over a Unix-domain socket. To control an instance over the network or connect a GUI from another machine, expose the control listener with `--control-address HOST:PORT` (or `marasi service control start`), set a control password via `MARASI_PASSWORD`, and point the CLI at it with `--remote http://HOST:PORT`. Run `marasi service control pair` to print a QR code for pairing another device.

Use `marasi events` to watch live activity from an instance (pass `--last-event-id` to replay recent events after a disconnect). Any client routed through the proxy can also open `http://marasi/cert` to download the CA certificate in DER format, or `http://marasi/view/<pair-id>` for a browser view of a stored response at its original URL without contacting the origin.

## Configuration

### Global flags

| Flag | Environment variable | Purpose |
| --- | --- | --- |
| `--instance NAME` | `MARASI_INSTANCE` | Service instance name (default `default`) |
| `--config-dir PATH` | `MARASI_CONFIG_DIR` | Config directory |
| `--remote URL` | `MARASI_REMOTE` | Control listener URL of a remote instance |
| `--password PASS` | `MARASI_PASSWORD` | Control password for `--remote` or `service start` |
| `--json` | - | Print JSON output (NDJSON for streaming commands) |

### Config directory

By default, Marasi stores configuration and projects in:

| Platform | Default location |
| --- | --- |
| macOS | `~/Library/Application Support/Marasi` |
| Linux | `$XDG_CONFIG_HOME/Marasi` (or `~/.config/Marasi`) |
| Windows | `%AppData%\Marasi` |

Inside the config directory:

- `projects/<name>.marasi`: named SQLite project files.
- `marasi_config.yaml`: machine configuration (update settings, upstream proxies, Chrome paths and profiles, message/body size limits).
- `marasi_cert.pem` and `marasi_key.pem`: shared CA certificate and private key.
- `wordlists/` and `templates/`: Armory wordlists and report templates.
- `instances/`: per-instance control sockets (`<name>.sock`) and process logs (`<name>.log`).

## Go library

Configure a proxy in Go with `marasi.New` and its option functions. Shared data types live in `domain`, and `service` provides the HTTP control API server and client.

```sh
go get github.com/tfkr-ae/marasi
```

See the [Go package reference](https://pkg.go.dev/github.com/tfkr-ae/marasi) and [`cmd/marasi/service.go`](cmd/marasi/service.go) for a full setup example.

## Development

Requires Go 1.26+.

| Command | Purpose |
| --- | --- |
| `make build` | Build `dist/marasi` for this machine (no CGO) |
| `make install` | Build and install `marasi` to `~/.local/bin` (`PREFIX` overrides `~/.local`) |
| `make all` | Cross-compile all targets in [Makefile](Makefile) |
| `make check` | Run `gofmt`, `go vet`, `staticcheck`, and `go test -short ./...` |
| `make test` | Run the full Go test suite (`go test ./...`) |

Make sets `GOWORK=off` automatically; if a parent `go.work` excludes this checkout, pass `GOWORK=off` for direct `go` commands.

### Conformance harness

Marasi's proxy behavior is tested against eight Docker-based conformance suites covering HTTP/1.1, HTTP/2, gRPC, Connect, SSE/NDJSON streaming, and WebSockets (Autobahn).

Run the required suites with `make conformance` and the WebSocket suite with `make autobahn`. See [`docs/conformance.md`](docs/conformance.md) for suite details, narrowing flags, baselines, and CI setup.

## Documentation

- [Conformance harness](docs/conformance.md)
- [Glossary](GLOSSARY.md)
- [Architecture decisions](docs/adr/)
- [Coding standards](CODING_STANDARDS.md)
- [Live CLI verification guide](.agents/skills/verify-marasi/SKILL.md)
- [GitHub Discussions](https://github.com/tfkr-ae/marasi/discussions)
