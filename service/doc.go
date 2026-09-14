// Package service is the HTTP control API for a running Marasi instance.
//
// Server serves the API. Client dials the instance Unix socket.
// The routes are GET /traffic, GET /traffic/{id}, GET /events, POST /service/stop, and POST /project/open.
//
// GET /events is live and has no replay. Subscribe first, then fetch /traffic.
// Events can arrive before the matching row is visible until handlers run
// after the database write.
package service
