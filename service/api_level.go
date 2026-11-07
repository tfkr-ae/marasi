package service

// APILevel is the control API level that GET /service/status reports as
// "api". A client decides from it alone whether it supports an instance.
//
// Bump it by one when a route, request field, response field or event that a
// client relies on is removed or changes meaning. Adding a route, field or
// event never bumps it. A bump is a breaking change for clients: mention it in
// the release notes.
const APILevel = 1
