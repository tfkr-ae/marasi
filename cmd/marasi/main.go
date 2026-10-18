// Command marasi starts and stops named proxy instances and inspects
// captured HTTP traffic through a Unix-socket control API.
package main

import "os"

// Build identity, set with -ldflags -X by the Makefile (VERSION, CHANNEL,
// SIGNING_PUBLIC_KEY). A local build leaves buildChannel and signingPublicKey
// empty: it is a development build with no channel and cannot install updates.
var (
	version = "dev"
	// buildChannel is the channel this build was published on: "release",
	// "nightly", or "" for a development build.
	buildChannel = ""
	// signingPublicKey is the base64 line of the Marasi minisign public key
	// (ADR-0048) that updates must be signed with, or "" when none is built in.
	signingPublicKey = ""
)

func main() {
	if err := executeCommand(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
