# Marasi verification feature map

The primary surface is the `marasi` CLI. It controls a detached proxy service through a per-instance Unix-domain socket, including on Windows. Launch, doctor, isolate, and cleanup are in [SKILL.md](../SKILL.md). The control API and Go library are secondary surfaces and do not replace CLI proof for these features.

| Feature | User entry points | Proof file |
| --- | --- | --- |
| Service lifecycle | `service start`, `service status`, `service stop`, `service list`, `service restart` | [service lifecycle](service-lifecycle.md) |
| Update | `update`, `update check`, `update channel`, `update install`, `update reinstall` | [update](update.md) |
| Proxy listener control | `listener status`, `listener address`, `listener stop`, `listener start`, `listener update` | [proxy listener control](proxy-listener-control.md) |
| HTTP traffic capture | Configure a client with `proxy_listener`, then make HTTP or HTTPS requests | [HTTP traffic capture](http-traffic-capture.md) |
| Captured traffic inspection | `traffic list`, list queries (`-q`) and pagination, `traffic get`, `traffic metadata get`, `traffic metadata update`, `traffic count`, `traffic types`, `traffic sitemap` (one level, or several with `--node`/`--scope`), `traffic validate`, `traffic bulk metadata set`, `traffic bulk metadata clear`, `traffic bulk delete` | [captured traffic inspection](captured-traffic-inspection.md) |
| Notes | `notes set`, `notes get`, `notes list`, list pagination, `notes clear`, `traffic bulk note append`, `traffic bulk note clear` | [notes](notes.md) |
| Project open | `project open --name`, `project open --path`, `project switch`, `project list` | [project open](project-open.md) |
| Events | `events`, `traffic bulk highlight` | [events](events.md) |
| Launchpad | `launchpad create`, `launchpad list`, `launchpad get`, `launchpad update`, `launchpad link`, `launchpad unlink`, `launchpad launch`, `launchpad pending`, `launchpad status`, `launchpad cancel`, `traffic bulk link`, `traffic bulk unlink` | [launchpad](launchpad.md) |
| Background jobs | `jobs list`, `jobs rebuild` | [jobs](jobs.md) |
| Client settings | `settings list`, `settings get`, `settings set`, `settings delete` | [settings](settings.md) |
| Test case | `test-case create`, `test-case list`, `test-case get`, `test-case list-requests`, `test-case update`, `test-case delete`, `test-case link`, `test-case unlink`, `test-case checklist` | [test case](test-case.md) |
| Finding | `finding create`, `finding list`, `finding get`, `finding list-requests`, `finding update`, `finding delete`, `finding link`, `finding unlink` | [finding](finding.md) |
| Artifact | `artifact upload`, `artifact list`, `artifact get`, `artifact download`, `artifact delete` | [artifact](artifact.md) |
| Checkpoint | `checkpoint list`, `checkpoint get`, `checkpoint forward`, `checkpoint drop`, `checkpoint intercept`, `checkpoint websocket-intercept` | [checkpoint](checkpoint.md) |
| Chrome | `chrome path add`, `chrome path list`, `chrome path remove`, `chrome profile add`, `chrome profile list`, `chrome profile remove`, `chrome start` | [chrome](chrome.md) |
| Waypoint | `waypoint list`, `waypoint add`, `waypoint update`, `waypoint remove` | [waypoint](waypoint.md) |
| Wordlist | `wordlist list`, `wordlist preview`, `wordlist add`, `wordlist remove` | [wordlist](wordlist.md) |
| Armory | `armory template create`, `armory template list`, `armory template get`, `armory template update`, `armory template delete`, `armory run validate`, `armory run create`, `armory run list`, `armory run get`, `armory run start`, `armory run cancel`, `armory run traffic`, `armory run delete` | [armory](armory.md) |
| Extension | `extension list`, `extension get`, `extension update`, `extension logs`, `extension settings get`, `extension settings set`, `extension call`, `extension enable`, `extension disable`, `extension api` | [extension](extension.md) |
| Report | `report template list`, `report template add`, `report template remove`, `report template restore`, `report export` | [report](report.md) |
| WebSocket | `websocket list`, `websocket get`, `websocket messages`, `traffic websocket`, `websocket inject`, `websocket close` | [websocket](websocket.md) |
| Scope check | `scope check` | [scope check](scope.md) |
| Proxy logs | `logs`, list pagination with `--limit` and `--cursor` | [proxy logs](logs.md) |
| CA certificate | `certificate get`, `--format pem` or `der` | [CA certificate](certificate.md) |
| Browser view and Marasi's own pages | `http://marasi/cert`, `http://marasi/view/{uuid}` through the proxy listener | [browser view](browser-view.md) |
| Agent skill | `skill` | [agent skill](agent-skill.md) |

Every `traffic bulk` command takes exactly one selection: `--id` (repeatable) or `--ids-file PATH` (`-` reads stdin), or `-q TEXT` with a required `--as-of UUID` and optional repeatable `--exclude`. No selection, both forms, `-q` without `--as-of`, or `--as-of`/`--exclude` with ids fails before any request; prove that by running it against an instance name that is not running and requiring the usage message, not `instance … is not running`. Human mode prints `N pairs <verb>` (`1 pair …`) on stderr; `--json` prints the route's `{"count":n}`. None asks for confirmation.

Human list tables print their column names on a line above the rows and again below them. An empty list prints nothing (ADR-0044). Skip those two lines when you assert on rows.

When a change touches one row, read that feature file and cover every entry point it names. There is no bundled script. A command that inspects or acts on captured traffic is proven only after real client traffic has gone through the proxy.
