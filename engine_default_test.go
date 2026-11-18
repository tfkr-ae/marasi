//go:build !h1engine

package marasi

import (
	"errors"
	"testing"
)

func TestNew_RunsOnMartianByDefault(t *testing.T) {
	t.Run("should serve on Martian unless the build or a test chooses the h1 engine", func(t *testing.T) {
		proxy, err := New()
		if err != nil {
			t.Fatalf("creating proxy: %v", err)
		}
		if _, ok := proxy.engine.(*martianEngine); !ok {
			t.Fatalf("wanted: *martianEngine\ngot: %T", proxy.engine)
		}
	})
}

func TestWithH1Engine(t *testing.T) {
	t.Run("should refuse to replace an engine an earlier option configured", func(t *testing.T) {
		if _, err := New(WithBasePipeline(), withH1Engine()); !errors.Is(err, errEngineConfigured) {
			t.Fatalf("wanted: %v\ngot: %v", errEngineConfigured, err)
		}
	})
}
