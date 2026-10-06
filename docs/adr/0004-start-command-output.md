# Start prints instance name and proxy address on stderr

`service stop` is silent because stop has nothing to inspect. Start prints two stderr lines, then exits, with stdout empty:

```
instance work started
proxy listener started on 127.0.0.1:53142
```

The proxy line is the existing startup message. The instance line uses the instance name, including `default`. No project name, socket path, or PID. Start stays quiet until those two lines or an error. A failed start writes the error to stderr and does not print the success lines.

With `--json`, a successful start writes one CLI-specific JSON object on stdout and does not print the human lines:

```json
{"instance":"work","proxy_listener":"127.0.0.1:53142"}
```

Start has no control API body to pass through. Traffic `--json` stays an API passthrough per ADR-0002. A failed `start --json` still exits non-zero, writes the error to stderr, and prints no JSON object.
