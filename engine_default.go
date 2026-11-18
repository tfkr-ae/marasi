//go:build !h1engine

package marasi

// newEngine returns the engine a proxy runs on by default: Martian, until the
// switch. Build with -tags h1engine to run on Marasi's own engine instead.
func newEngine(proxy *Proxy) engine {
	return newMartianEngine(proxy)
}
