package marasi

import (
	"bufio"
	"net/http"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// TestProxy_CheckpointInterceptCountsPair checks that a pair Checkpoint
// holds, at its request or its response, is in the dashboard's maintained
// intercepted count once it is stored (ticket 23).
func TestProxy_CheckpointInterceptCountsPair(t *testing.T) {
	forward := func(harness *checkpointAbandonHarness, item domain.CheckpointItem) {
		if err := harness.proxy.ForwardCheckpoint(item.ID, CheckpointForward{}); err != nil {
			t.Errorf("forwarding: %v", err)
		}
	}
	capture := func(t *testing.T, harness *checkpointAbandonHarness) {
		t.Helper()
		client := harness.send(t)
		defer client.Close()
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		response.Body.Close()
		receiveCheckpointItem(t, harness.held, "the hold")
		if err := harness.proxy.flushDBWrites(); err != nil {
			t.Fatalf("flushing writes: %v", err)
		}
	}
	assertCounts := func(t *testing.T, harness *checkpointAbandonHarness, pairs, intercepted int) {
		t.Helper()
		counts, err := harness.proxy.StatsRepo.DashboardCounts()
		if err != nil {
			t.Fatalf("DashboardCounts() error = %v", err)
		}
		if counts.Pairs != pairs || counts.Intercepted != intercepted || !counts.Complete[domain.JobInterceptedCount] {
			t.Fatalf("DashboardCounts() = %d pairs, %d intercepted, complete %v; want %d, %d, true",
				counts.Pairs, counts.Intercepted, counts.Complete[domain.JobInterceptedCount], pairs, intercepted)
		}
		if got, err := harness.proxy.StatsRepo.CountIntercepted(); err != nil || got != intercepted {
			t.Fatalf("CountIntercepted() = %d, %v, want %d", got, err, intercepted)
		}
	}

	t.Run("should count a pair held at its request", func(t *testing.T) {
		harness := newCheckpointAbandonHarness(t, forward)
		harness.proxy.SetIntercept(true)
		capture(t, harness)
		assertCounts(t, harness, 1, 1)
	})

	t.Run("should count a pair held at its response", func(t *testing.T) {
		harness := newCheckpointAbandonHarness(t, forward)
		checkpoint, ok := harness.proxy.GetExtension("checkpoint")
		if !ok {
			t.Fatal("getting checkpoint extension")
		}
		if err := checkpoint.ExecuteLua(`function interceptResponse(response) return true end`); err != nil {
			t.Fatalf("updating checkpoint: %v", err)
		}
		capture(t, harness)
		assertCounts(t, harness, 1, 1)
	})
}
