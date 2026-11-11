// Package repolint holds no code of its own. Its test parses every Go file in
// the module and fails on a violation of a mechanical rule in
// CODING_STANDARDS.md, so `go test ./...` enforces those rules without a
// separate linter. The rules that need judgement stay with the reviewer.
//
// Violations that predate the check are listed in baseline.txt. After fixing
// some of them, or when a reviewer accepts a new entry, regenerate it with:
//
//	GOWORK=off go test ./internal/repolint -update
package repolint
