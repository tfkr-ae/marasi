//go:build bench1m

package fixture_test

// With the bench1m tag, the tests build projects with full-size bodies.
func init() { testMaxBody = 0 }
