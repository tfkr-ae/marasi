package marasi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModifierGroup_Controls(t *testing.T) {
	controls := map[string]func(*Pair){
		"respond": func(pair *Pair) { pair.Respond(newResponse(pair.Request, http.StatusTeapot, nil)) },
		"drop":    func(pair *Pair) { pair.Drop() },
		"stop":    func(pair *Pair) { pair.Stop() },
	}
	for name, control := range controls {
		t.Run("should end the group without an error on "+name, func(t *testing.T) {
			var ran []string
			group := NewModifierGroup()
			group.Add(func(_ *Proxy, pair *Pair) error { ran = append(ran, "first"); control(pair); return nil })
			group.Add(func(*Proxy, *Pair) error { ran = append(ran, "second"); return nil })

			pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
			if err := group.Modify(&Proxy{}, pair); err != nil {
				t.Fatalf("wanted: nil\ngot: %v", err)
			}
			if want := []string{"first"}; !reflect.DeepEqual(want, ran) {
				t.Fatalf("wanted:\n%v\ngot:\n%v", want, ran)
			}
		})
	}

	t.Run("should report a drop", func(t *testing.T) {
		pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
		if pair.Dropped() {
			t.Fatal("wanted a new pair not dropped")
		}
		pair.Drop()
		if !pair.Dropped() {
			t.Fatal("wanted the pair dropped")
		}
	})

	t.Run("should mark the response Respond gave as Marasi's", func(t *testing.T) {
		pair := NewTestPair(httptest.NewRequest(http.MethodGet, "http://marasi.app", nil))
		if pair.FromMarasi() {
			t.Fatal("wanted a new pair's response not Marasi's")
		}
		res := newResponse(pair.Request, http.StatusTeapot, nil)
		pair.Respond(res)
		if pair.Response != res || !pair.FromMarasi() {
			t.Fatalf("wanted the pair to hold Marasi's response\ngot: %v, from Marasi %v", pair.Response, pair.FromMarasi())
		}
	})
}

// controlOrigin counts the requests that reach it and answers "origin".
func controlOrigin(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "origin")
	}))
	t.Cleanup(origin.Close)
	return origin, &hits
}

func TestPairControls_ThroughProxy(t *testing.T) {
	t.Run("should answer a request Respond answered without reaching the origin or the response chain", func(t *testing.T) {
		origin, hits := controlOrigin(t)
		var later, responses atomic.Int32
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error {
				pair.Respond(newResponse(pair.Request, http.StatusTeapot, []byte("marasi")))
				return nil
			})
			proxy.AddRequestPairModifier(func(*Proxy, *Pair) error { later.Add(1); return nil })
			proxy.AddResponsePairModifier(func(*Proxy, *Pair) error { responses.Add(1); return nil })
		})

		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("sending through proxy: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusTeapot || string(body) != "marasi" {
			t.Fatalf("wanted: 418 marasi\ngot: %d %s", res.StatusCode, body)
		}
		if hits.Load() != 0 || later.Load() != 0 || responses.Load() != 0 {
			t.Fatalf("wanted no origin hit, no later request modifier and no response modifier\ngot: %d, %d, %d", hits.Load(), later.Load(), responses.Load())
		}
	})

	t.Run("should not send a dropped request upstream", func(t *testing.T) {
		origin, hits := controlOrigin(t)
		var later, responses atomic.Int32
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error { pair.Drop(); return nil })
			proxy.AddRequestPairModifier(func(*Proxy, *Pair) error { later.Add(1); return nil })
			proxy.AddResponsePairModifier(func(*Proxy, *Pair) error { responses.Add(1); return nil })
		})

		if res, err := client.Get(origin.URL); err == nil {
			res.Body.Close()
		}
		if hits.Load() != 0 || later.Load() != 0 || responses.Load() != 0 {
			t.Fatalf("wanted no origin hit, no later request modifier and no response modifier\ngot: %d, %d, %d", hits.Load(), later.Load(), responses.Load())
		}
	})

	t.Run("should send a stopped request upstream without the rest of the request chain", func(t *testing.T) {
		origin, hits := controlOrigin(t)
		var later, responses atomic.Int32
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddRequestPairModifier(func(_ *Proxy, pair *Pair) error { pair.Stop(); return nil })
			proxy.AddRequestPairModifier(func(*Proxy, *Pair) error { later.Add(1); return nil })
			proxy.AddResponsePairModifier(func(*Proxy, *Pair) error { responses.Add(1); return nil })
		})

		res, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("sending through proxy: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(body) != "origin" {
			t.Fatalf("wanted: origin\ngot: %s", body)
		}
		if hits.Load() != 1 || later.Load() != 0 || responses.Load() != 1 {
			t.Fatalf("wanted one origin hit, no later request modifier and the response chain run\ngot: %d, %d, %d", hits.Load(), later.Load(), responses.Load())
		}
	})

	t.Run("should close the client connection on a dropped response", func(t *testing.T) {
		origin, _ := controlOrigin(t)
		var later atomic.Int32
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error { pair.Drop(); return nil })
			proxy.AddResponsePairModifier(func(*Proxy, *Pair) error { later.Add(1); return nil })
		})

		res, err := client.Get(origin.URL)
		if err == nil {
			res.Body.Close()
			t.Fatalf("wanted the connection closed\ngot: %d", res.StatusCode)
		}
		if later.Load() != 0 {
			t.Fatal("wanted no later response modifier")
		}
	})

	t.Run("should tell the response chain a response Marasi made from the origin's", func(t *testing.T) {
		origin, _ := controlOrigin(t)
		var seen []bool
		client := servePairProxy(t, func(proxy *Proxy) {
			proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error {
				seen = append(seen, pair.FromMarasi())
				return nil
			})
			proxy.AddResponsePairModifier(func(_ *Proxy, pair *Pair) error {
				if !strings.HasSuffix(pair.Request.URL.Path, "/answer") {
					return nil
				}
				pair.Respond(newResponse(pair.Request, http.StatusTeapot, []byte("marasi")))
				seen = append(seen, pair.FromMarasi())
				return nil
			})
		})

		for _, path := range []string{"/", "/answer"} {
			res, err := client.Get(origin.URL + path)
			if err != nil {
				t.Fatalf("sending through proxy: %v", err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if path == "/answer" && (res.StatusCode != http.StatusTeapot || string(body) != "marasi") {
				t.Fatalf("wanted: 418 marasi\ngot: %d %s", res.StatusCode, body)
			}
		}
		if want := []bool{false, false, true}; !reflect.DeepEqual(want, seen) {
			t.Fatalf("wanted:\n%v\ngot:\n%v", want, seen)
		}
	})
}
