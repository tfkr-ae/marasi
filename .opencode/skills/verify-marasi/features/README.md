# Marasi verification feature map

The primary surface is the `marasi` CLI. It controls a detached proxy service through a per-instance Unix socket. The control API and Go library are secondary surfaces and do not replace CLI proof for these features.

| Feature | User entry points | Proof file |
| --- | --- | --- |
| Service lifecycle | `service start`, `service status`, `service stop` | [service lifecycle](service-lifecycle.md) |
| Proxy listener control | `listener status`, `listener address`, `listener stop`, `listener start`, `listener update` | [proxy listener control](proxy-listener-control.md) |
| HTTP traffic capture | Configure a client with `proxy_listener`, then make HTTP or HTTPS requests | [HTTP traffic capture](http-traffic-capture.md) |
| Captured traffic inspection | `traffic list`, list filters and pagination, `traffic get` | [captured traffic inspection](captured-traffic-inspection.md) |
| Project open | `project open --name`, `project open --path`, `project switch` | [project open](project-open.md) |
| Events | `events` | [events](events.md) |
| Launchpad | `launchpad create`, `launchpad list`, `launchpad get`, `launchpad update`, `launchpad link`, `launchpad launch` | [launchpad](launchpad.md) |
| Test case | `test-case create`, `test-case list`, `test-case get`, `test-case update`, `test-case delete`, `test-case link`, `test-case unlink`, `test-case checklist` | [test case](test-case.md) |
| Finding | `finding create`, `finding list`, `finding get`, `finding update`, `finding delete`, `finding link`, `finding unlink` | [finding](finding.md) |

When a change touches one row, read that feature file and cover every affected entry point. The bundled helper proves service lifecycle, one HTTP capture, live `events` subscription, filtered listing, and detail inspection. It does not cover every listener transition, `listener address`, HTTPS interception, pagination, project open, launchpad, test cases, or findings.
