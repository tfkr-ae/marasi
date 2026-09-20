# Marasi verification feature map

The primary surface is the `marasi` CLI. It controls a detached proxy service through a per-instance Unix socket. The control API and Go library are secondary surfaces and do not replace CLI proof for these features.

| Feature | User entry points | Proof file |
| --- | --- | --- |
| Service lifecycle | `service start`, `service status`, `service stop` | [service lifecycle](service-lifecycle.md) |
| Proxy listener control | `listener status`, `listener address`, `listener stop`, `listener start`, `listener update` | [proxy listener control](proxy-listener-control.md) |
| HTTP traffic capture | Configure a client with `proxy_listener`, then make HTTP or HTTPS requests | [HTTP traffic capture](http-traffic-capture.md) |
| Captured traffic inspection | `traffic list`, list filters and pagination, `traffic get`, `traffic metadata get`, `traffic metadata update` | [captured traffic inspection](captured-traffic-inspection.md) |
| Notes | `notes set`, `notes list`, list pagination, `notes clear` | [notes](notes.md) |
| Project open | `project open --name`, `project open --path`, `project switch` | [project open](project-open.md) |
| Events | `events` | [events](events.md) |
| Launchpad | `launchpad create`, `launchpad list`, `launchpad get`, `launchpad update`, `launchpad link`, `launchpad launch` | [launchpad](launchpad.md) |
| Test case | `test-case create`, `test-case list`, `test-case get`, `test-case update`, `test-case delete`, `test-case link`, `test-case unlink`, `test-case checklist` | [test case](test-case.md) |
| Finding | `finding create`, `finding list`, `finding get`, `finding update`, `finding delete`, `finding link`, `finding unlink` | [finding](finding.md) |
| Artifact | `artifact upload`, `artifact get`, `artifact download`, `artifact delete` | [artifact](artifact.md) |
| Checkpoint | `checkpoint list`, `checkpoint get`, `checkpoint forward`, `checkpoint drop`, `checkpoint intercept`, `checkpoint websocket-intercept` | [checkpoint](checkpoint.md) |
| Chrome | `chrome path add`, `chrome path list`, `chrome path remove`, `chrome profile add`, `chrome profile list`, `chrome profile remove`, `chrome start` | [chrome](chrome.md) |
| Waypoint | `waypoint list`, `waypoint add`, `waypoint update`, `waypoint remove` | [waypoint](waypoint.md) |
| Wordlist | `wordlist list`, `wordlist preview`, `wordlist add`, `wordlist remove` | [wordlist](wordlist.md) |
| Armory | `armory template create`, `armory template list`, `armory template get`, `armory template update`, `armory template delete`, `armory run validate`, `armory run create`, `armory run list`, `armory run get`, `armory run start`, `armory run cancel`, `armory run traffic`, `armory run delete` | [armory](armory.md) |
| Extension | `extension list`, `extension get`, `extension update`, `extension logs`, `extension settings get`, `extension settings set`, `extension call`, `extension enable`, `extension disable` | [extension](extension.md) |

When a change touches one row, read that feature file and cover every affected entry point. The bundled helper proves service lifecycle, one HTTP capture, live `events` subscription, filtered listing, and detail inspection. It does not cover every listener transition, `listener address`, HTTPS interception, pagination, project open, launchpad, test cases, findings, artifacts, notes, metadata, Checkpoint, Chrome, waypoints, wordlists, Armory, or extensions.
