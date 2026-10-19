# Marasi

Marasi is an application security testing proxy controlled through named service instances.

## Language

**Service instance**:
A named running copy of the Marasi service that controls one open project.
_Avoid_: Service, daemon, or process when referring to the named running copy. The start command is not the service instance.

**Project**:
A Marasi workspace stored in a `.marasi` file and identified by its canonical absolute path.

**Client setting**:
A JSON value that a client stores in a project under its own client name and a key, such as `marasi-app` and `ledger.exclusion`. Marasi keeps it but never reads its meaning.
_Avoid_: preference, config, app settings

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

**Pair number**:
The position of a request/response pair in its project's capture order, starting at 1. It never changes and is never reused, so deleting pairs leaves gaps.
_Avoid_: row number, index, rowid, sequence, id

**Note**:
An optional annotation on a request/response pair, identified by that pair's UUID. A pair has at most one.
_Avoid_: comment, row note, traffic comment

**Metadata**:
A document of extra data stored with a request/response pair.
_Avoid_: tags, highlight, properties

**Selection**:
The request/response pairs that one bulk operation applies to: either listed pairs, or every pair that matches a query and content-type exclusion, up to the newest pair the client had seen. A query selection is refused while a background job its query depends on is unfinished.
_Avoid_: checked rows, batch, bulk target

**Sitemap**:
A project's traffic arranged as a tree, first by origin and then by path segment, with a count of pairs and of server errors at each node.
_Avoid_: site tree, target tree

**Highlight**:
A number from 1 to 8 that a client puts on a request/response pair to mark it. A pair has at most one. Marasi gives the number no meaning; the client decides what each number looks like and stands for.
_Avoid_: colour, tag, label, slot

**Query**:
A text expression that narrows one collection's list to the items that match it. It is not stored and has no id.
_Avoid_: filter, search string, FTS

**Content-type exclusion**:
A list of content types, each exact or a whole major type such as `image/*`, that a client passes with a traffic read to hide request/response pairs of those types. Marasi does not store it. A pair with no content type yet is never hidden by it.
_Avoid_: filter, content type filter, app filters

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
A complete text or binary message, or a single ping, pong, or close, on a WebSocket connection. Stored in the open project and identified by its own UUID. A text or binary message may arrive in several WebSocket frames.
_Avoid_: frame, stream, checkpoint item

**WebSocket frame**:
One protocol unit on the wire that carries all or part of a WebSocket message. It is not stored on its own and has no id.
_Avoid_: message, packet, chunk

**WebSocket extension**:
A protocol extension that the client offers and the upstream accepts in the `Sec-WebSocket-Extensions` handshake header. The proxy forwards only permessage-deflate (RFC 7692). It removes any other offer and records the name on the WebSocket connection. Not an Extension.
_Avoid_: extension on its own, plugin

**Waypoint**:
A pairing of an original host:port with an override host:port, stored in the open project and identified by the original host:port.
_Avoid_: host override, redirect, DNS override, way point

**Launchpad**:
A named group of request/response pairs, each an ordinary traffic pair linked to the group rather than copied. A launchpad may have no members.
_Avoid_: Repeater, folder, tag

**Launchpad entry**:
A request/response pair linked to a launchpad. The same pair can be an entry in several launchpads.
_Avoid_: item, member row, copy

**Entry order**:
The order of a launchpad's entries, which changes only when a researcher moves entries. Every entry a launchpad gains goes last.
_Avoid_: sort order, position, original order

**Move**:
Taking entries out of one launchpad and putting them at a chosen place in another, or at a new place in the same one, keeping their entry order relative to each other. A move never copies, and is refused if the target already holds one of the pairs, or if any of the pairs is a pending launch and the target is another launchpad.
_Avoid_: drag, reorder when the entries change launchpad, copy

**Placement**:
Where a move puts its entries in the target: before a given entry of the target, or after its last entry. A placement before an entry that is not in the target, or that is itself moved, is invalid.
_Avoid_: anchor, position, drop target

**Launch**:
Sending a raw request from a launchpad through the proxy. Its request/response pair joins the launchpad, and the launch id is that pair's UUID. A launch is pending until it is completed, cancelled, or failed. While a launch is pending, its pair cannot be removed from its launchpad or deleted.
_Avoid_: replay, repeat, send

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

**Hook**:
A global function in an extension's Lua that the proxy calls by name, such as processRequest or startup. `marasi extension api hooks` lists them.
_Avoid_: callback, handler

**Logbook**:
The GUI name for the combined test case and finding list. It is not stored and has no id.
_Avoid_: using logbook as a control API collection or as a synonym for test case or finding

**Proxy log**:
A message recorded by the proxy and stored in the open project, identified by UUID. It has a severity and may name a request/response pair or an extension.
_Avoid_: instance log, extension log, event

**Background job**:
Work that Marasi does on an open project after opening it, such as building the search index or the sitemap. It stops when the project closes and resumes from where it stopped on the next open. A client can ask for it to be rebuilt.
_Avoid_: task, migration, maintenance

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

### Updates

**Installed build**:
The Marasi executable on this machine that the CLI runs and that every service instance started from it runs.
_Avoid_: binary, install, app

**Channel**:
The stream of builds an installed build follows: release or nightly. It starts as the channel of the build that was installed, and a user can switch it. A development build has no channel.
_Avoid_: track, ring, stable, beta

**Release**:
A build of Marasi from main, named by the date it was cut.
_Avoid_: stable release, version

**Nightly**:
The latest daily build of Marasi. Only one nightly exists at a time; each one replaces the last.
_Avoid_: beta, preview, snapshot

**Update**:
A build on the installed build's channel that is newer than the installed build and replaces it once installed through a service instance. It is never older; going back to an older release is a reinstall.
_Avoid_: upgrade, patch, self-update

**Reinstall**:
An explicit replacement of the installed build with the latest build on its channel, even when that build is older. Projects opened in a newer build may not open in it.
_Avoid_: downgrade, rollback

**Restart**:
A service instance handing its name, open project, and proxy listener address to a successor started from the installed build, then exiting. If the successor does not become ready, the instance keeps them and keeps running.
_Avoid_: reload, relaunch

**Successor**:
The service instance a restart starts from the installed build to take over the restarting instance's name, open project, and proxy listener address. It becomes that instance once it reports ready.
_Avoid_: child, replacement, new instance
