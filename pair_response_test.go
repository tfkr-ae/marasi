package marasi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestResponseChainThroughProxy(t *testing.T) {
	t.Run("should answer a dropped request with an empty 200 and no response chain", func(t *testing.T) {
		var originHits atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originHits.Add(1)
		}))
		defer origin.Close()

		var responseRuns atomic.Int32
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(proxy *Proxy, pair *Pair) error {
				pair.Drop()
				return nil
			})
			proxy.AddResponsePairModifier(func(proxy *Proxy, pair *Pair) error {
				responseRuns.Add(1)
				pair.Response.Header.Set("X-Response-Chain", "ran")
				return nil
			})
		})

		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("sending request: %v", err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)

		if res.StatusCode != http.StatusOK || len(body) != 0 {
			t.Errorf("\nwanted:\nthe blank 200\ngot:\n%d %q", res.StatusCode, body)
		}
		if got := res.Header.Get("X-Response-Chain"); got != "" {
			t.Errorf("\nwanted:\nno X-Response-Chain header\ngot:\n%q", got)
		}
		if got := responseRuns.Load(); got != 0 {
			t.Errorf("\nwanted:\nresponse chain run 0 times\ngot:\n%d", got)
		}
		if got := originHits.Load(); got != 0 {
			t.Errorf("\nwanted:\norigin reached 0 times\ngot:\n%d", got)
		}
	})

	t.Run("should run the response chain on the pair the request chain set up", func(t *testing.T) {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "origin")
		}))
		defer origin.Close()

		var requestPair, responsePair *Pair
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(proxy *Proxy, pair *Pair) error {
				requestPair = pair
				pair.Metadata = map[string]any{"from": "request"}
				return nil
			})
			proxy.AddResponsePairModifier(ResponseFilterModifier)
			proxy.AddResponsePairModifier(func(proxy *Proxy, pair *Pair) error {
				responsePair = pair
				if pair.Timing.Responded.IsZero() {
					pair.Response.Header.Set("X-Problem", "no response time")
				}
				if pair.Metadata["from"] != "request" {
					pair.Response.Header.Set("X-Problem", "metadata lost")
				}
				return nil
			})
		})

		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("sending request: %v", err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)

		if string(body) != "origin" {
			t.Errorf("\nwanted:\n%q\ngot:\n%q", "origin", body)
		}
		if got := res.Header.Get("X-Problem"); got != "" {
			t.Errorf("\nwanted:\nno problem\ngot:\n%s", got)
		}
		if requestPair == nil || responsePair != requestPair {
			t.Errorf("\nwanted:\none pair on both chains\ngot:\nrequest %p, response %p", requestPair, responsePair)
		}
	})
}
