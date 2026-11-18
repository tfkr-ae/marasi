//go:build h1engine

package marasi

import "testing"

func TestNew_RunsOnTheH1EngineWithTheTag(t *testing.T) {
	t.Run("should serve on the h1 engine when built with the h1engine tag", func(t *testing.T) {
		proxy, err := New()
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		if _, ok := proxy.engine.(*h1Engine); !ok {
			t.Fatalf("wanted: *h1Engine\ngot: %T", proxy.engine)
		}
	})
}
