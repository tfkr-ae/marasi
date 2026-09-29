// Package service is the HTTP control API for a running Marasi instance.
//
// Server serves the API. Client dials the instance Unix socket.
// addRoutes registers the control handlers. Server also registers
// /listener/, POST /project/open, and GET /events.
// Project-bound handlers run only after admittingMux admits the request.
//
// GET /events is live and has no replay. Subscribe first, then read the row.
// Events can arrive before the matching row is visible until handlers run
// after the database write.
package service
