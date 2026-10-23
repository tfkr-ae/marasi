package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tfkr-ae/marasi"
)

func TestUpstreamProxiesEmbedded(t *testing.T) {
	t.Run("should add, use, list and remove through the exported constructor, storing the selection with no events", func(t *testing.T) {
		lifecycle, proxy, dir := newTestProjectLifecycle(t)
		path := canonicalProjectPath(t, filepath.Join(dir, "embedded.marasi"))
		if err := lifecycle.Open(context.Background(), path); err != nil {
			t.Fatalf("opening project: %v", err)
		}
		proxies := NewUpstreamProxies(proxy)
		ctx := context.Background()

		if _, err := proxies.Add(ctx, marasi.UpstreamProxyConfig{Name: "burp", URL: "http://u:p@127.0.0.1:8080"}); err != nil {
			t.Fatalf("adding upstream proxy: %v", err)
		}
		if err := proxies.Use(ctx, "burp"); err != nil {
			t.Fatalf("using upstream proxy: %v", err)
		}
		if got, want := proxies.Selection(), (UpstreamProxySelection{Selected: "burp", Configured: true}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		repository, err := proxy.GetConfigRepo()
		if err != nil {
			t.Fatalf("getting config repository: %v", err)
		}
		stored, err := marasi.LoadUpstreamProxySelection(repository)
		if err != nil || stored != "burp" {
			t.Fatalf("\nwanted:\nburp\ngot:\n%q, %v", stored, err)
		}

		items, err := proxies.List(ctx)
		want := UpstreamProxy{Name: "burp", Scheme: "http", Host: "127.0.0.1", Port: 8080, HasCredentials: true}
		if err != nil || len(items) != 1 || items[0] != want {
			t.Fatalf("\nwanted:\n[%+v]\ngot:\n%+v, %v", want, items, err)
		}

		items, err = proxies.Remove(ctx, "burp")
		if err != nil || len(items) != 0 {
			t.Fatalf("\nwanted:\n[]\ngot:\n%+v, %v", items, err)
		}
		if got, want := proxies.Selection(), (UpstreamProxySelection{Selected: "burp", Configured: false}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})
}
