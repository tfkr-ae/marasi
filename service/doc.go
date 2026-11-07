// Package service is the HTTP control API for a running Marasi instance.
//
// Server serves the API. Client dials the instance Unix socket.
// addRoutes registers the control handlers. Server also registers
// /listener/, POST /project/open, and GET /events.
// Project-bound handlers run only after admittingMux admits the request.
//
// GET /events is live and has no replay. Subscribe first, then read the row.
// Traffic events can arrive before the matching row is persisted.
// log.added follows a successful database insert.
// Checkpoint events describe in-flight items, not stored rows.
//
// # API level
//
// GET /service/status reports the control API level as "api", an integer
// that started at 1 (APILevel). A client decides from it alone whether it
// supports an instance. Bump APILevel by one in the same change that removes
// a route, request field, response field or event a client relies on, or
// changes what one means. Adding a route, field or event never bumps it. Say
// so in the release notes when it bumps.
package service
