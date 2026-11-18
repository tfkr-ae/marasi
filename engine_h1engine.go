//go:build h1engine

package marasi

// newEngine returns Marasi's own HTTP/1.1 engine. The h1engine build tag is
// internal: conformance runs use it (make conformance TAGS=h1engine) until
// the switch makes this engine the only one.
func newEngine(proxy *Proxy) engine {
	return newH1Engine(proxy)
}
