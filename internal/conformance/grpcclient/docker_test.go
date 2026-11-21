package grpcclient

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestInteropImage(t *testing.T) {
	t.Run("should name the Dockerfile's content in the tag, so a cached image is rebuilt when it changes", func(t *testing.T) {
		sum := sha256.Sum256(dockerfile)
		want := "marasi-conformance-grpc-java:" + InteropVersion + "-" + hex.EncodeToString(sum[:])[:12]
		if InteropImage != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, InteropImage)
		}
	})

	t.Run("should leave protobuf-javalite out of the classpath, whose DurationProto has no descriptor", func(t *testing.T) {
		if !strings.Contains(string(dockerfile), "-DexcludeArtifactIds=protobuf-javalite") {
			t.Fatalf("\nwanted:\nthe Dockerfile to exclude protobuf-javalite\ngot:\n%s", dockerfile)
		}
	})
}
