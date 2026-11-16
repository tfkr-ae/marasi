package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Defaults for a suite whose own client and origin fixtures record each
// case, such as h1-origin and h2-origin.
const (
	// CaseTimeout is how long the client or a fixture waits for the peer's
	// next message.
	CaseTimeout = 5 * time.Second
	// CaseSettle is how long the client watches a connection after the
	// case's last message, to see whether the peer closes it.
	CaseSettle = 250 * time.Millisecond
)

// RecordCatalogue records every case ID in ids that job selects, in order,
// one at a time. record records ids[i]; its recording is stored as JSON.
func RecordCatalogue(ctx context.Context, job Job, ids []string, record func(ctx context.Context, i int) (any, error)) (Recordings, error) {
	recordings := Recordings{}
	for i, id := range ids {
		if !Selected(id, job.Cases, job.Exclude, job.Approaches) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return recordings, err
		}
		recording, err := record(ctx, i)
		if err != nil {
			return recordings, fmt.Errorf("recording case %s: %w", id, err)
		}
		data, err := json.Marshal(recording)
		if err != nil {
			return recordings, err
		}
		recordings[id] = data
		if job.Log != nil {
			fmt.Fprintf(job.Log, "%s: recorded\n", id)
		}
	}
	return recordings, nil
}

// CatalogueOrder orders case IDs as ids lists them; an ID ids does not
// list comes after every listed one, in ID order.
func CatalogueOrder(ids []string) func(a, b string) bool {
	order := make(map[string]int, len(ids))
	for i, id := range ids {
		order[id] = i
	}
	rank := func(id string) int {
		if i, ok := order[id]; ok {
			return i
		}
		return len(order)
	}
	return func(a, b string) bool {
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a < b
	}
}
