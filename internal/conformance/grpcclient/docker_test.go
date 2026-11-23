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

	t.Run("should pin maven-dependency-plugin by full coordinates instead of resolving the dependency prefix", func(t *testing.T) {
		text := string(dockerfile)
		if !strings.Contains(text, "org.apache.maven.plugins:maven-dependency-plugin:3.7.0:copy-dependencies") {
			t.Fatalf("\nwanted:\npinned maven-dependency-plugin coordinates\ngot:\n%s", text)
		}
		if strings.Contains(text, " dependency:copy-dependencies") {
			t.Fatalf("\nwanted:\nno unpinned dependency: prefix\ngot:\n%s", text)
		}
	})

	t.Run("should keep Maven transfer warnings visible instead of passing -q", func(t *testing.T) {
		text := string(dockerfile)
		if !strings.Contains(text, "mvn -B -ntp ") {
			t.Fatalf("\nwanted:\nmvn -B -ntp so transfer warnings stay in the log\ngot:\n%s", text)
		}
		if strings.Contains(text, "mvn -q") {
			t.Fatalf("\nwanted:\nno mvn -q hiding transfer errors\ngot:\n%s", text)
		}
	})

	t.Run("should list Google's Maven Central mirror ahead of central for both artifacts and plugins", func(t *testing.T) {
		text := string(dockerfile)
		const mirror = "https://maven-central.storage-download.googleapis.com/maven2"
		if strings.Count(text, mirror) != 2 {
			t.Fatalf("\nwanted:\n%s in both repositories and pluginRepositories\ngot:\n%s", mirror, text)
		}
	})
}
