package marasi

import "net/http"

// runRequestPairModifier runs a request pair modifier on req as the proxy's
// chain does, so the pair's values reach req's context for the response
// modifiers that still read it there.
func runRequestPairModifier(proxy *Proxy, modifier PairModifier, req *http.Request) error {
	pair, loaded := loadPair(req, nil)
	err := modifier(proxy, pair)
	storePair(pair, loaded, req)
	return err
}
