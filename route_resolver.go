package marasi

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// routeResolver turns the open project's upstream proxy selection and this
// machine's list in marasi_config.yaml into a Route. It reads the YAML again
// only when the file's modification time or size changes, and the proxy
// asks it once per new connection to the origin.
type routeResolver struct {
	mu        sync.Mutex
	selection string
	timeout   time.Duration

	// The list as last read, and the file stamp it was read at.
	loaded  bool
	stamp   fileStamp
	entries []UpstreamProxyConfig
	readErr error
}

// fileStamp is what the resolver compares to tell a changed file.
type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

// UpstreamProxyStatus is the open project's upstream proxy selection.
type UpstreamProxyStatus struct {
	// Name is the selected upstream proxy, or "" for none.
	Name string
	// Configured reports whether Name is in this machine's list. It is true
	// when nothing is selected.
	Configured bool
}

// choose sets the selection and returns the route it resolves to now.
func (resolver *routeResolver) choose(configDir, name string) *Route {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.selection = name
	return resolver.resolveLocked(configDir)
}

// resolve returns the route for the current selection, reading the list
// again if the file changed.
func (resolver *routeResolver) resolve(configDir string) *Route {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.resolveLocked(configDir)
}

// status reports the selection and whether this machine's list has it.
func (resolver *routeResolver) status(configDir string) UpstreamProxyStatus {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.selection == "" {
		return UpstreamProxyStatus{Configured: true}
	}
	resolver.refreshLocked(configDir)
	_, found := resolver.entryLocked(resolver.selection)
	return UpstreamProxyStatus{Name: resolver.selection, Configured: found}
}

func (resolver *routeResolver) resolveLocked(configDir string) *Route {
	name := resolver.selection
	if name == "" {
		// Direct needs no list, so a broken file cannot stop it.
		return DirectRoute()
	}
	resolver.refreshLocked(configDir)
	route := &Route{Name: name, timeout: resolver.timeout}
	if resolver.readErr != nil {
		route.failure = route.fail(UpstreamProxyInvalid, "marasi_config.yaml cannot be read", resolver.readErr)
		return route
	}
	entry, found := resolver.entryLocked(name)
	if !found {
		route.failure = route.fail(UpstreamProxyNotConfigured, "not configured on this machine", nil)
		return route
	}
	parsed, address, err := ParseUpstreamProxyURL(entry.URL)
	if err != nil {
		// The error names the field at fault and never quotes the URL.
		route.failure = route.fail(UpstreamProxyInvalid, err.Error(), err)
		return route
	}
	route.Scheme, route.Host, route.Port = address.Scheme, address.Host, address.Port
	route.credentials = parsed.User
	route.ca = entry.CA
	if entry.CA != "" {
		if _, err := ParseUpstreamProxyCA(entry.CA); err != nil {
			route.failure = route.fail(UpstreamProxyInvalid, err.Error(), err)
		}
	}
	return route
}

func (resolver *routeResolver) entryLocked(name string) (UpstreamProxyConfig, bool) {
	for _, entry := range resolver.entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return UpstreamProxyConfig{}, false
}

// refreshLocked reads the list again when the file's stamp changed. An
// empty configDir has no list.
func (resolver *routeResolver) refreshLocked(configDir string) {
	if configDir == "" {
		resolver.loaded, resolver.entries, resolver.readErr = true, nil, nil
		return
	}
	path := filepath.Join(configDir, "marasi_config.yaml")
	var stamp fileStamp
	info, err := os.Stat(path)
	switch {
	case err == nil:
		stamp = fileStamp{exists: true, modTime: info.ModTime(), size: info.Size()}
	case !errors.Is(err, os.ErrNotExist):
		resolver.loaded, resolver.entries, resolver.readErr = false, nil, err
		return
	}
	if resolver.loaded && stamp == resolver.stamp {
		return
	}
	resolver.loaded, resolver.stamp = true, stamp
	if !stamp.exists {
		resolver.entries, resolver.readErr = nil, nil
		return
	}
	cfg, err := ReadConfig(configDir)
	if err != nil {
		resolver.entries, resolver.readErr = nil, err
		return
	}
	resolver.entries, resolver.readErr = cfg.UpstreamProxies, nil
}

// SelectUpstreamProxy makes name the upstream proxy new connections to the
// origin go through, or a direct connection when name is "". It dials
// nothing. A name missing from this machine's list fails every request
// closed until it is added or the selection changes.
func (proxy *Proxy) SelectUpstreamProxy(name string) {
	proxy.applyRoute(proxy.routes.choose(proxy.ConfigDir, name))
}

// UpstreamProxyStatus returns the selection and whether it is configured.
func (proxy *Proxy) UpstreamProxyStatus() UpstreamProxyStatus {
	return proxy.routes.status(proxy.ConfigDir)
}

// resolveRoute returns the route for a new connection, swapping the current
// route first when the selection or the list changed what it resolves to.
func (proxy *Proxy) resolveRoute() *Route {
	return proxy.applyRoute(proxy.routes.resolve(proxy.ConfigDir))
}

// applyRoute installs resolved unless the current route already reaches the
// origin the same way, and returns the route now current.
func (proxy *Proxy) applyRoute(resolved *Route) *Route {
	proxy.routeMu.Lock()
	defer proxy.routeMu.Unlock()
	if current := proxy.CurrentRoute(); sameRoute(current, resolved) {
		return current
	}
	proxy.SetRoute(resolved)
	return resolved
}
