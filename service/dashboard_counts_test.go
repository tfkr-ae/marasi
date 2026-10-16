package service

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// TestDashboardCountsFollowControlAPIWriters checks that every control API
// write of the intercepted metadata key keeps the maintained intercepted
// count exact (ticket 23).
func TestDashboardCountsFollowControlAPIWriters(t *testing.T) {
	t.Run("should follow PUT metadata, bulk set and clear of intercepted, and bulk delete", func(t *testing.T) {
		p := setupBulkProject(t)
		a := p.insertPair(t, "GET")
		b := p.insertPair(t, "GET")
		c := p.insertPair(t, "GET")
		want := func(pairs, intercepted int) {
			t.Helper()
			counts, err := p.repo.DashboardCounts()
			if err != nil {
				t.Fatalf("DashboardCounts() error = %v", err)
			}
			if counts.Pairs != pairs || counts.Intercepted != intercepted || !counts.Complete[domain.JobInterceptedCount] {
				t.Fatalf("DashboardCounts() = %d pairs, %d intercepted, complete %v; want %d, %d, true",
					counts.Pairs, counts.Intercepted, counts.Complete[domain.JobInterceptedCount], pairs, intercepted)
			}
		}
		request := func(method, path, body string) {
			t.Helper()
			if response := requestControlAPI(p.server, method, path, body); response.Code != http.StatusOK {
				t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body)
			}
		}
		want(3, 0)

		request(http.MethodPut, "/traffic/"+a.String()+"/metadata", `{"intercepted":true}`)
		want(3, 1)
		request(http.MethodPut, "/traffic/"+a.String()+"/metadata", `{"intercepted":true,"other":1}`)
		want(3, 1)

		request(http.MethodPost, "/traffic/bulk/metadata",
			fmt.Sprintf(`{"selection":{"ids":[%q,%q,%q]},"key":"intercepted","value":true}`, a, b, c))
		want(3, 3)
		request(http.MethodPost, "/traffic/bulk/metadata",
			fmt.Sprintf(`{"selection":{"ids":[%q]},"key":"intercepted","value":false}`, c))
		want(3, 2)
		request(http.MethodPost, "/traffic/bulk/metadata",
			fmt.Sprintf(`{"selection":{"ids":[%q]},"key":"intercepted","clear":true}`, b))
		want(3, 1)

		request(http.MethodPut, "/traffic/"+a.String()+"/metadata", `{}`)
		want(3, 0)
		request(http.MethodPut, "/traffic/"+b.String()+"/metadata", `{"intercepted":true}`)
		want(3, 1)

		request(http.MethodPost, "/traffic/bulk/delete", fmt.Sprintf(`{"selection":{"ids":[%q,%q]}}`, a, b))
		want(1, 0)
	})
}
