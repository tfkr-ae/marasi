package h1client

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestProbeImage(t *testing.T) {
	t.Run("should name the Dockerfile's content in the tag, so a cached image is rebuilt when it changes", func(t *testing.T) {
		sum := sha256.Sum256(dockerfile)
		want := "marasi-conformance-http11probe:" + ProbeCommit + "-" + hex.EncodeToString(sum[:])[:12]
		if ProbeImage != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, ProbeImage)
		}
	})

	t.Run("should run Http11Probe on the pinned .NET runtime stage rather than keeping the SDK image", func(t *testing.T) {
		text := string(dockerfile)
		if !strings.Contains(text, "FROM mcr.microsoft.com/dotnet/runtime:10.0@sha256:") {
			t.Fatalf("\nwanted:\npinned .NET runtime final stage\ngot:\n%s", text)
		}
	})
}
