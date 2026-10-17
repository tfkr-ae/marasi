//go:build bench1m

package ledgerbench_test

// With the bench1m tag, the tests build projects with full-size bodies.
func init() { testMaxBody = 0 }
