# Marasi

[![Go reference](https://pkg.go.dev/badge/github.com/tfkr-ae/marasi.svg)](https://pkg.go.dev/github.com/tfkr-ae/marasi)

<img src="images/logo.svg" width="150" alt="Marasi">

Marasi is an application security testing proxy controlled through the `marasi` CLI.
It captures HTTP, HTTPS, and WebSocket traffic, stores assessment data in SQLite
projects, and supports Lua extensions. The proxy is also available as a Go library.

[marasi.app](https://marasi.app)

## Features

- Captured traffic with complete HTTP requests and responses, metadata, and notes.
- Checkpoint to hold, forward, and drop in-flight requests, responses, and WebSocket messages.
- WebSocket inspection, frame injection, and live connection control.
- Launchpad to group captured pairs and send modified raw HTTP requests.
- Armory request templates with payload positions and wordlist-driven runs.
- Test cases, findings, and artifacts to record assessment work and link evidence.
- Report generation with configurable templates.
- Lua extensions with settings, logs, and callable functions.
- Scope rules and waypoints for traffic filtering and host-and-port routing.
- Chrome profiles and launch with proxy settings.
- SQLite `.marasi` projects that store traffic and assessment data.

New projects include the `compass`, `checkpoint`, and `workshop` Lua extensions.
`compass` manages scope rules. `checkpoint` supplies rules for which traffic
Checkpoint holds. `workshop` provides a Lua development environment.

## Service mode and CLI

Each named service instance runs as a detached process and owns one open project.
`service start` returns after the instance is ready. The CLI sends HTTP control API
requests over a per-instance Unix-domain socket on macOS, Linux, and Windows.

The control listener and proxy listener are separate. Stopping the proxy listener
leaves the service instance available for inspection. `service stop` shuts down
the service instance.

Projects are SQLite `.marasi` files. A project can be owned by only one service
instance at a time. `project open` changes the open project without restarting
the proxy listener.

### Build and test commands

The source requires Go 1.26 or later. These commands run from the repository root.

| Command | Result |
| --- | --- |
| `make build` | Builds `dist/marasi` for this machine without CGO. |
| `make install` | Builds and installs `marasi` in `$HOME/.local/bin`. `PREFIX` overrides `$HOME/.local`. |
| `make windows-amd64` | Builds `dist/marasi-windows-amd64.exe`. |
| `make all` | Builds the targets listed by `TARGETS` in [Makefile](Makefile). |
| `make test` | Runs `go test ./...`. |

A build without Make uses `go build -o marasi ./cmd/marasi`, or
`go build -o marasi.exe ./cmd/marasi` on Windows. If a parent `go.work` excludes
the checkout, `GOWORK=off` disables that workspace for builds and tests.

### CLI flags

`marasi --help` lists commands. Each command's `--help` lists its subcommands,
arguments, and flags. The examples below use an installed `marasi` binary.

The global flags are:

| Flag | Meaning |
| --- | --- |
| `--instance NAME` | Selects a service instance. Defaults to `default`. |
| `--config-dir PATH` | Selects the machine config directory. |
| `--json` | Prints command output as JSON. `events` emits one JSON object per line. |

### Service commands

This start command creates or opens the named project `assessment` and starts
the service instance `assessment` with a proxy listener at `127.0.0.1:8080`:

```sh
marasi --instance assessment service start --project-name assessment --address 127.0.0.1 --port 8080 --json
```

`--port 0` requests an allocated port. The start result reports the bound address.
Without a project flag, `service start` opens the named project `scratchpad`.

Other service and project commands include:

```sh
marasi --instance assessment service status --json
marasi service list --json
marasi project list --json
marasi --instance assessment project open --name another-assessment --json
marasi --instance assessment service stop --json
```

### Traffic and CA certificate commands

These commands inspect stored traffic, subscribe to live events, and export the
loaded CA certificate:

```sh
marasi --instance assessment traffic list --limit 20 --json
marasi --instance assessment traffic list --host example.com --method GET --status-code 200 --json
marasi --instance assessment traffic get UUID --json
marasi --instance assessment events --json
marasi --instance assessment certificate get > marasi-ca.pem
```

`UUID` denotes an ID returned by `traffic list`. Raw request and response bytes
are base64-encoded in JSON output. Events are live notifications with no replay.
Traffic events can arrive before the matching row is persisted.

HTTPS inspection requires the client to trust Marasi's CA certificate. The
exported CA certificate contains no private key. All service instances that use
the same config directory share the CA.

### Storage and configuration

The default config directory is `Marasi` under `os.UserConfigDir()`:

| Platform | Default location |
| --- | --- |
| macOS | `~/Library/Application Support/Marasi` |
| Linux | `$XDG_CONFIG_HOME/Marasi`, or `~/.config/Marasi` when unset |
| Windows | `%AppData%\Marasi` |

Named projects live at `<config-dir>/projects/<name>.marasi`. Project files store
traffic, notes, metadata, proxy logs, extensions, waypoints, launchpads, Armory
templates and runs, test cases, findings, and artifacts.

The config directory also contains `config.yaml`, shared CA material, Chrome
configuration, `wordlists/`, and report `templates/`. Each service instance has
an `instances/<name>.sock` control socket and an `instances/<name>.log` process log.
Proxy logs are stored in the project, not in the process log.

The control socket path, including its terminating byte, cannot exceed 104 bytes.
Windows also requires an absolute socket path. A short absolute `--config-dir`
avoids both limits.

## Library mode

The Go library uses `marasi.New` and option functions to configure the proxy.
Callback data types live in `domain`. The `service` package provides the HTTP
control API and its Unix-socket client.

```sh
go get github.com/tfkr-ae/marasi
```

- [Go package reference](https://pkg.go.dev/github.com/tfkr-ae/marasi) documents the library API.
- [CLI startup implementation](cmd/marasi/service.go) shows how the service connects the proxy and project resources.

### Project documentation

- [Glossary](GLOSSARY.md) defines the project's terms.
- [Architecture decisions](docs/adr/) record API and lifecycle contracts.
- [Live CLI verification guide](.agents/skills/verify-marasi/SKILL.md) covers verification with isolated service instances.
- [GitHub Discussions](https://github.com/tfkr-ae/marasi/discussions) hosts questions and feedback.
