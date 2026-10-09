# Marasi

Marasi is an application security testing proxy controlled through named service instances.

## Language

**Service instance**:
A named running copy of the Marasi service that controls one open project.
_Avoid_: Service, daemon, or process when referring to the named running copy. The start command is not the service instance.

**Project**:
A Marasi workspace stored in a `.marasi` file and identified by its canonical absolute path.

**Open project**:
The project whose persisted data and configuration a service instance currently controls. A service instance always has exactly one.
_Avoid_: Active project, current project, closed project, projectless instance.

**Project ownership**:
The exclusive claim held by one service instance while it controls an open project.
_Avoid_: Project lock when referring to the domain rule rather than its filesystem implementation.

**Control listener**:
The listener through which clients send control API requests to one service instance.

**Proxy listener**:
The listener through which browsers and other clients send traffic to a service instance for proxying.

**CA certificate**:
The public certificate of the authority shared by every service instance that uses one config directory. It is not a private key and not a per-host leaf.
_Avoid_: certificate, cert, marasi.cert

**Traffic**:
The request/response pairs persisted for the open project.
_Avoid_: Flow, exchange, HTTP history when referring to this collection.

**Request/response pair**:
One captured HTTP request and its matching response, identified by a single UUID.
_Avoid_: Flow, exchange, transaction.

**Note**:
An optional annotation on a request/response pair, identified by that pair's UUID. A pair has at most one.
_Avoid_: comment, row note, traffic comment

**Metadata**:
A document of extra data stored with a request/response pair.
_Avoid_: tags, highlight, properties

**Query**:
A text expression that narrows one collection's list to the items that match it. It is not stored and has no id.
_Avoid_: filter, search string, FTS

**Checkpoint**:
The intercept capability of a service instance. It is not stored and has no id.
_Avoid_: intercept as the product name, using checkpoint as a synonym for the checkpoint extension

**Checkpoint item**:
An in-flight request, response, or WebSocket message held by Checkpoint. Identified by the request/response pair UUID or the WebSocket message UUID. Not stored in the open project.
_Avoid_: intercepted item, intercept, paused traffic, held flow

**Scope**:
The live include and exclude rules on a service instance, plus whether traffic that matches no rule is allowed. Not stored as its own record.
_Avoid_: report scope, compass scope, filter

**Scope rule**:
A host or url pattern on a scope that either includes or excludes.
_Avoid_: filter, regex

**Scope check**:
A question to the live scope about whether a URL is in scope, together with whether Compass is enabled. It is not stored and has no id, and it does not send traffic.
_Avoid_: dry run, scope test, scope tester

**WebSocket connection**:
The proxied socket opened by one request/response pair, identified by its own UUID. The pair's UUID finds that connection. It is not the connection's id.
_Avoid_: stream, socket, session

**WebSocket message**:
A frame on a WebSocket connection, stored in the open project and identified by its own UUID.
_Avoid_: stream, checkpoint item

**Waypoint**:
A pairing of an original host:port with an override host:port, stored in the open project and identified by the original host:port.
_Avoid_: host override, redirect, DNS override, way point

**Launchpad**:
A named group of request/response pairs, each an ordinary traffic pair linked to the group rather than copied. A launchpad may have no members.
_Avoid_: Repeater, folder, tag

**Armory**:
The request-template and run capability of a service instance. It is not stored and has no id.
_Avoid_: Intruder, fuzzer, using Armory as a control API collection or as a synonym for template or run

**Armory template**:
A reusable raw HTTP request with payload positions, stored in the open project and identified by UUID.
_Avoid_: Intruder template, payload template, request snippet

**Payload position**:
A marked slot in an Armory template.
_Avoid_: insertion point, fuzz point, placeholder

**Attack type**:
The strategy that maps wordlists onto payload positions for an Armory run. One of harpoon, broadside, tandem, or maelstrom.
_Avoid_: sniper, battering ram, pitchfork, cluster bomb

**Armory run**:
An execution of an Armory template snapshot, stored in the open project and identified by UUID. It has an attack type, wordlists, concurrency limit, and lifecycle status.
_Avoid_: job, campaign, attack when referring to the stored record

**Armory entry**:
An ordinary request/response pair linked to an Armory run rather than copied.
_Avoid_: result, hit, copy

**Test case**:
An assessment work item recorded in the open project, identified by UUID. It may link to request/response pairs and artifacts. Distinct from a predefined test case.
_Avoid_: logbook when referring to the record

**Predefined test case**:
A title, description, and category used as a starting point for a test case. It has no id and is not stored in the open project.
_Avoid_: test case when referring to the template

**Finding**:
A security vulnerability or discovery recorded in the open project, identified by UUID. It belongs to at most one test case. It may link to request/response pairs and artifacts.
_Avoid_: vuln, issue, logbook

**Artifact**:
A file attached to either one test case or one finding, never both, identified by UUID.
_Avoid_: artefact, attachment

**Extension**:
A Lua program stored in the open project, identified by UUID, with a unique name. Every new project includes compass, checkpoint, and workshop.
_Avoid_: plugin, script, special extension

**Logbook**:
The GUI name for the combined test case and finding list. It is not stored and has no id.
_Avoid_: using logbook as a control API collection or as a synonym for test case or finding

**Proxy log**:
A message recorded by the proxy and stored in the open project, identified by UUID. It has a severity and may name a request/response pair or an extension.
_Avoid_: instance log, extension log, event

**Event**:
A named live notification emitted by a service instance at the moment something happens. It is not stored with the open project and is not replayed.
_Avoid_: Stream, message, log.

**Chrome path**:
A configured Chrome or Chromium executable location on this machine, tagged with an operating system.
_Avoid_: chrome dir, browser path, chrome_dirs

**Chrome profile**:
A named Chrome user-data identity registered in the machine config and shared across projects and service instances that use that config dir. Chrome creates the directory on first start.
_Avoid_: browser profile, user-data-dir when referring to the Marasi-managed name

**Wordlist**:
A named payload file that lives in the machine wordlists directory and is identified by its filename. Shared across projects and service instances that use that config dir. It is the file itself, not a pointer to a file elsewhere.
_Avoid_: dictionary, payload file, word list

**Report**:
A rendered assessment document from one report template and the open project's test cases and findings. It is not stored and has no id.
_Avoid_: export file, document, draft report

**Report template**:
A named file in the machine templates directory, identified by its filename. Shared across projects and service instances that use that config dir. It is the file itself, not a pointer to a file elsewhere.
_Avoid_: Armory template, predefined test case, template
