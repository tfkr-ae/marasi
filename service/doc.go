// Package service is the Marasi Service control plane.
//
// GET /events is a live notification stream, not a source of truth. It has no
// replay, filtering, or persistence. Clients should subscribe first:
//
//  1. connect to /events and wait until the connected comment has been received
//  2. fetch the needed pages from /traffic
//  3. merge buffered request and response events with those results
//  4. correlate by UUID and event type
//
// That sequence reduces the initial race but is not gapless. Marasi currently
// invokes handlers after queuing an asynchronous database write, so an event
// fired immediately before subscription might not yet appear in a concurrent
// /traffic read. Clients that require stronger convergence may poll the newest
// traffic page periodically.
package service
