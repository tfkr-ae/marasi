package fixture

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// bodyKind is the shape of a body.
type bodyKind uint8

const (
	bodyNone bodyKind = iota
	bodyJSON
	bodyHTML
	bodyXML
	bodyJS
	bodyCSS
	bodyText
	bodyBinary
)

// contentType is one response media type the fixture uses.
type contentType struct {
	name  string
	kind  bodyKind
	magic string // file signature of a binary body
	group string // json, html, js, css, image, font, octet, text, other
}

// weighted picks an index from cumulative weights.
type weighted struct {
	cumulative []float64
}

func newWeighted(weights []float64) weighted {
	w := weighted{cumulative: make([]float64, len(weights))}
	total := 0.0
	for i, v := range weights {
		total += v
		w.cumulative[i] = total
	}
	return w
}

func (w weighted) pick(rng *rand.Rand) int {
	r := rng.Float64() * w.cumulative[len(w.cumulative)-1]
	return sort.SearchFloat64s(w.cumulative, r)
}

// contentTypes holds about 100 distinct types. The weights apply to the 2xx
// pairs of sessions, and are set so that, with the bursts, error pages,
// redirects and excluded stretches added, the whole project comes to about
// 40% JSON, 12% HTML, 15% JS, 5% CSS, 15% images, 3% fonts, 2% octet-stream
// and 3% text. Redirects are all text/plain, as the proxy records them, so
// text comes out higher.
var contentTypes, contentTypeWeights = buildContentTypes()

func buildContentTypes() ([]contentType, weighted) {
	var types []contentType
	var weights []float64
	add := func(group string, share float64, entries ...contentType) {
		for _, entry := range entries {
			entry.group = group
			types = append(types, entry)
			weights = append(weights, share/float64(len(entries)))
		}
	}
	add("json", 27, contentType{name: "application/json", kind: bodyJSON})
	vendor := []contentType{
		{name: "application/problem+json", kind: bodyJSON},
		{name: "application/hal+json", kind: bodyJSON},
		{name: "application/ld+json", kind: bodyJSON},
		{name: "application/vnd.api+json", kind: bodyJSON},
	}
	for i := range 53 {
		vendor = append(vendor, contentType{
			name: fmt.Sprintf("application/vnd.%s.v%d+json", words[(i*7)%len(words)], 1+i%3),
			kind: bodyJSON,
		})
	}
	add("json", 12, vendor...)
	add("html", 14, contentType{name: "text/html", kind: bodyHTML})
	add("js", 13.5, contentType{name: "application/javascript", kind: bodyJS})
	add("js", 9, contentType{name: "text/javascript", kind: bodyJS})
	add("css", 6.3, contentType{name: "text/css", kind: bodyCSS})
	add("image", 4.5, contentType{name: "image/png", kind: bodyBinary, magic: "\x89PNG\r\n\x1a\n"})
	add("image", 3.75, contentType{name: "image/jpeg", kind: bodyBinary, magic: "\xff\xd8\xff\xe0"})
	add("image", 1.5, contentType{name: "image/gif", kind: bodyBinary, magic: "GIF89a"})
	add("image", 2.25, contentType{name: "image/webp", kind: bodyBinary, magic: "RIFF\x00\x00\x00\x00WEBP"})
	add("image", 1.5, contentType{name: "image/svg+xml", kind: bodyXML})
	add("image", 0.75, contentType{name: "image/x-icon", kind: bodyBinary, magic: "\x00\x00\x01\x00"})
	add("image", 0.75, contentType{name: "image/avif", kind: bodyBinary, magic: "\x00\x00\x00\x1cftypavif"})
	add("font", 0.78, contentType{name: "font/woff2", kind: bodyBinary, magic: "wOF2"})
	add("font", 0.26, contentType{name: "font/woff", kind: bodyBinary, magic: "wOFF"})
	add("font", 0.13, contentType{name: "font/ttf", kind: bodyBinary, magic: "\x00\x01\x00\x00"})
	add("font", 0.065, contentType{name: "application/font-woff", kind: bodyBinary, magic: "wOFF"})
	add("font", 0.065, contentType{name: "font/otf", kind: bodyBinary, magic: "OTTO"})
	add("octet", 1.5, contentType{name: "application/octet-stream", kind: bodyBinary, magic: "\x7fELF"})
	add("text", 0.5, contentType{name: "text/plain", kind: bodyText})
	add("text", 0.5,
		contentType{name: "text/xml", kind: bodyXML},
		contentType{name: "application/xml", kind: bodyXML},
		contentType{name: "text/csv", kind: bodyText},
		contentType{name: "text/event-stream", kind: bodyText})
	add("other", 5,
		contentType{name: "application/x-www-form-urlencoded", kind: bodyText},
		contentType{name: "application/pdf", kind: bodyBinary, magic: "%PDF-1.7"},
		contentType{name: "application/grpc-web+proto", kind: bodyBinary, magic: "\x00\x00"},
		contentType{name: "application/x-protobuf", kind: bodyBinary, magic: "\x0a\x00"},
		contentType{name: "video/mp4", kind: bodyBinary, magic: "\x00\x00\x00\x18ftypmp42"},
		contentType{name: "audio/mpeg", kind: bodyBinary, magic: "ID3"},
		contentType{name: "application/wasm", kind: bodyBinary, magic: "\x00asm"},
		contentType{name: "application/manifest+json", kind: bodyJSON},
		contentType{name: "text/markdown", kind: bodyText},
		contentType{name: "application/zip", kind: bodyBinary, magic: "PK\x03\x04"},
		contentType{name: "application/x-ndjson", kind: bodyText},
		contentType{name: "application/rss+xml", kind: bodyXML},
		contentType{name: "application/atom+xml", kind: bodyXML},
		contentType{name: "text/x-component", kind: bodyText},
		contentType{name: "application/csp-report", kind: bodyJSON},
		contentType{name: "application/vnd.ms-excel", kind: bodyBinary, magic: "\xd0\xcf\x11\xe0"},
		contentType{name: "application/msword", kind: bodyBinary, magic: "\xd0\xcf\x11\xe0"},
		contentType{name: "text/calendar", kind: bodyText},
		contentType{name: "application/graphql-response+json", kind: bodyJSON},
		contentType{name: "multipart/form-data", kind: bodyText})
	return types, newWeighted(weights)
}

func contentTypeIndex(name string) int {
	for i, ct := range contentTypes {
		if ct.name == name {
			return i
		}
	}
	panic("unknown content type " + name)
}

// excludedContentTypes are types in the seeded content-type exclusion. The
// excluded stretches use only these.
var excludedContentTypes = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
	"font/woff2", "font/woff", "text/css", "application/javascript", "application/octet-stream",
}

// Named hosts. The top ten carry about 60% of pairs; the first carries about 25%.
var topHosts = []string{
	"app.example.com", "api.example.com", "cdn.example.com", "auth.example.com", "static.example.com",
	"admin.example.com", "graph.example.com", "files.example.com", "m.example.com", "partners.example.com",
}

// Shares before the bursts, which add about 11% to api and auth.
var topHostShares = []float64{24, 8, 6, 3, 3, 2.5, 2, 1.5, 1.5, 1}

var tlds = []string{"com", "net", "io", "org", "dev", "co", "app", "cloud"}

// Planted sparse hosts, with fixed match counts.
const (
	spreadHost       = "spread.sparse.test"
	spreadHostPairs  = 400 // fewer in a small fixture, see newLayout
	oldHost          = "old.sparse.test"
	oldHostPairs     = 600
	rareOldHost      = "rare-old.sparse.test"
	rareOldHostPairs = 150
	sparseHostSuffix = ".internal.corp"
	rareTerm         = "quokkaventure"
	rareTermPairs    = 5
	rareNoteTerm     = "marmaladeridge"
	rareExtension    = "x-ray-tagger"
)

// The planned shape of one pair, decided before any content is generated.
type pairPlan struct {
	id          uuid.UUID
	at          time.Time
	latency     time.Duration
	host        int32
	https       bool
	method      string
	path        string
	status      int // -1 for a pair that never gets a response
	ctype       int
	respSize    int
	reqBodySize int
	codings     []string // response content codings stored encoded
	chunked     bool
	burst       int // 1-based armory run, 0 when not in a burst
	websocket   bool
	plantRare   bool
	extension   string
}

// stretch is a run of pairs, oldest index first.
type stretch struct{ start, length int }

func (s stretch) contains(i int) bool { return i >= s.start && i < s.start+s.length }

// layout places the fuzz bursts and the excluded stretches for a size.
type layout struct {
	bursts   []stretch
	excluded []stretch // the 1% stretch, then the 10% stretch
	// Matches of the planted sparse hosts. They are page-relative, so they
	// stay fixed except in a fixture too small to hold them.
	spreadPairs, oldPairs, rareOldPairs int
}

func newLayout(pairs int) layout {
	frac := func(f float64) int { return int(float64(pairs) * f) }
	return layout{
		// Two Armory runs: 50k and 60k pairs at 1M, about 11% of pairs.
		bursts: []stretch{{frac(0.20), frac(0.05)}, {frac(0.70), frac(0.06)}},
		// 10k and 100k pairs at 1M, positioned so a cursor just above each
		// one sees it as the newest pairs.
		excluded:     []stretch{{frac(0.85), frac(0.01)}, {frac(0.40), frac(0.10)}},
		spreadPairs:  min(spreadHostPairs, pairs/6),
		oldPairs:     min(oldHostPairs, pairs/10),
		rareOldPairs: min(rareOldHostPairs, pairs/20),
	}
}

func (l layout) burstOf(i int) int {
	for b, s := range l.bursts {
		if s.contains(i) {
			return b + 1
		}
	}
	return 0
}

func (l layout) inExcluded(i int) bool {
	for _, s := range l.excluded {
		if s.contains(i) {
			return true
		}
	}
	return false
}

// hostTable is every host the fixture uses, with pick weights.
type hostTable struct {
	names   []string
	pick    weighted
	special map[string]int32
}

func newHostTable(rng *rand.Rand, pairs int) hostTable {
	count := max(200, min(5000, pairs/250))
	names := append([]string(nil), topHosts...)
	weights := append([]float64(nil), topHostShares...)
	// The rest share what the top hosts leave by a Zipf distribution (s = 1.1).
	tailShare := 100.0
	for _, s := range topHostShares {
		tailShare -= s
	}
	tail := count - len(topHosts)
	harmonic := 0.0
	for k := 1; k <= tail; k++ {
		harmonic += math.Pow(float64(k), -1.1)
	}
	seen := map[string]bool{}
	for k := 1; k <= tail; k++ {
		var name string
		for name == "" || seen[name] {
			if k%40 == 0 {
				name = fmt.Sprintf("%s-%d%s", word(rng), k, sparseHostSuffix)
			} else {
				name = fmt.Sprintf("%s-%s.%s", word(rng), word(rng), tlds[rng.IntN(len(tlds))])
			}
		}
		seen[name] = true
		names = append(names, name)
		weights = append(weights, tailShare*math.Pow(float64(k), -1.1)/harmonic)
	}
	table := hostTable{names: names, pick: newWeighted(weights), special: map[string]int32{}}
	for _, name := range []string{spreadHost, oldHost, rareOldHost} {
		table.special[name] = int32(len(table.names))
		table.names = append(table.names, name)
	}
	return table
}

// planner decides every pair's shape from one seeded random stream.
type planner struct {
	rng    *rand.Rand
	ids    *idClock
	hosts  hostTable
	layout layout
	pairs  int
}

// planPairs returns the plan of every pair, oldest first, ending at end.
func planPairs(seed uint64, pairs int, end time.Time) ([]pairPlan, hostTable, layout) {
	rng := rand.New(rand.NewPCG(seed, 0x6d61726173690001))
	p := &planner{rng: rng, hosts: newHostTable(rng, pairs), layout: newLayout(pairs), pairs: pairs}
	plans := make([]pairPlan, pairs)

	// Session timeline: offsets in milliseconds from the first pair.
	offsets := make([]int64, pairs)
	var now int64
	sessionHost := int32(0)
	sessionLeft := 0
	outageLeft := 0
	for i := range plans {
		plan := &plans[i]
		burst := p.layout.burstOf(i)
		switch {
		case burst > 0:
			now += 2 + rng.Int64N(9)
			p.planBurst(plan, burst)
		default:
			if sessionLeft == 0 {
				now += int64(10*time.Minute/time.Millisecond) + rng.Int64N(int64(170*time.Minute/time.Millisecond))
				sessionHost = int32(p.hosts.pick.pick(rng))
				sessionLeft = 50 + int(rng.ExpFloat64()*400)
			} else {
				now += 1 + int64(rng.ExpFloat64()*300)
			}
			sessionLeft--
			host := sessionHost
			if rng.Float64() < 0.2 {
				host = int32(p.hosts.pick.pick(rng))
			}
			// Server errors cluster in outages covering about 0.75% of
			// pairs; another 0.2% are scattered.
			if outageLeft == 0 && rng.Float64() < 0.0075/85 {
				outageLeft = 20 + rng.IntN(130)
			}
			p.planSessionPair(plan, i, host, outageLeft > 0)
			if outageLeft > 0 {
				outageLeft--
			}
		}
		offsets[i] = now
	}

	// Planted sparse hosts.
	// Each planted host takes count distinct pairs between from and to,
	// outside the bursts and away from the other planted hosts.
	taken := map[int]bool{}
	place := func(host string, count int, from, to int) {
		h := p.hosts.special[host]
		for k := range count {
			i := from + (to-from)*k/count + rng.IntN(max(1, (to-from)/count))
			for i = min(i, to-1); taken[i] || p.layout.burstOf(i) > 0; {
				if i++; i >= to {
					i = from
				}
			}
			taken[i] = true
			plans[i].host = h
		}
	}
	place(spreadHost, p.layout.spreadPairs, 0, pairs)
	place(oldHost, p.layout.oldPairs, 0, pairs*45/100)
	place(rareOldHost, p.layout.rareOldPairs, 0, pairs*45/100)

	// Pairs carrying the rare text term, spread over time.
	for k := range rareTermPairs {
		i := pairs * (2*k + 1) / (2 * rareTermPairs)
		for p.layout.burstOf(i) > 0 || p.layout.inExcluded(i) {
			i++
		}
		plan := &plans[i]
		plan.plantRare = true
		plan.status = 200
		plan.ctype = contentTypeIndex("application/json")
		plan.codings, plan.chunked, plan.websocket = nil, false, false
		plan.respSize = max(plan.respSize, 512)
	}

	// Pairs still in flight, or that never got a response: 0.75%.
	for range pairs * 3 / 400 {
		i := rng.IntN(pairs)
		if p.layout.burstOf(i) > 0 || p.layout.inExcluded(i) || plans[i].plantRare || plans[i].websocket {
			continue
		}
		plans[i].status = -1
	}

	start := end.Add(-time.Duration(now) * time.Millisecond)
	p.ids = newIDClock(rng)
	for i := range plans {
		plans[i].at = start.Add(time.Duration(offsets[i]) * time.Millisecond)
		plans[i].id = p.ids.at(plans[i].at)
	}
	return plans, p.hosts, p.layout
}

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"}
var methodWeights = newWeighted([]float64{75, 17, 3, 1.5, 1.5, 1.5, 0.5})

func (p *planner) planBurst(plan *pairPlan, burst int) {
	rng := p.rng
	plan.burst = burst
	plan.host = []int32{1, 3}[burst-1] // api.example.com, then auth.example.com
	plan.https = true
	plan.method = "POST"
	plan.path = []string{"/api/v1/login", "/oauth/token"}[burst-1]
	plan.ctype = contentTypeIndex("application/json")
	switch r := rng.Float64(); {
	case r < 0.85:
		plan.status = 401
	case r < 0.95:
		plan.status = 429
	default:
		plan.status = 200
	}
	plan.respSize = logNormal(rng, 200, 0.3, 40, 2000)
	plan.reqBodySize = logNormal(rng, 300, 0.3, 60, 2000)
	plan.latency = time.Duration(logNormal(rng, 80, 0.4, 5, 2000)) * time.Millisecond
}

func (p *planner) planSessionPair(plan *pairPlan, i int, host int32, outage bool) {
	rng := p.rng
	plan.host = host
	plan.https = rng.Float64() >= 0.05
	plan.method = methods[methodWeights.pick(rng)]
	plan.latency = time.Duration(logNormal(rng, 120, 0.8, 1, 30_000)) * time.Millisecond
	if plan.method == "POST" || plan.method == "PUT" || plan.method == "PATCH" {
		plan.reqBodySize = int(1024 * math.Pow(20, rng.Float64()))
	}
	if rng.Float64() < 0.15 {
		switch r := rng.Float64(); {
		case r < 0.6:
			plan.extension = "compass"
		case r < 0.9:
			plan.extension = "workshop"
		case r < 0.995:
			plan.extension = "jwt-decoder"
		default:
			plan.extension = rareExtension
		}
	}

	if p.layout.inExcluded(i) {
		plan.status = 200
		plan.method = "GET"
		plan.reqBodySize = 0
		plan.ctype = contentTypeIndex(excludedContentTypes[rng.IntN(len(excludedContentTypes))])
		plan.respSize = logNormal(rng, 1500, 2.34, 64, 4<<20)
		plan.path = p.path(plan.ctype, host)
		return
	}

	// A few WebSocket upgrades, which the proxy records as octet-stream.
	if rng.Float64() < 1.0/20000 || (i%(p.pairs/5+1) == 17) {
		plan.websocket = true
		plan.method = "GET"
		plan.reqBodySize = 0
		plan.status = 101
		plan.ctype = contentTypeIndex("application/octet-stream")
		plan.path = "/ws/" + word(rng)
		return
	}

	switch r := rng.Float64() * 100; {
	case outage:
		plan.status = []int{500, 502, 503, 504}[rng.IntN(4)]
	// Shares within sessions. The bursts add their 401s and the excluded
	// stretches their 200s, which brings the whole project to about 70%
	// 2xx, 8% 3xx, 18% 4xx and 1% 5xx.
	case r < 0.3:
		plan.status = []int{500, 502, 503, 504}[rng.IntN(4)]
	case r < 79.8:
		plan.status = []int{200, 200, 200, 200, 200, 200, 200, 201, 204, 206}[rng.IntN(10)]
	case r < 90.1:
		plan.status = []int{301, 302, 302, 304}[rng.IntN(4)]
	default:
		plan.status = []int{400, 401, 403, 404, 404, 429}[rng.IntN(6)]
	}

	switch {
	case plan.status >= 300 && plan.status < 400:
		// The proxy records every redirect as text/plain.
		plan.ctype = contentTypeIndex("text/plain")
		if plan.status != 304 {
			plan.respSize = rng.IntN(200)
		}
		plan.path = p.path(contentTypeIndex("text/html"), host)
	case plan.status >= 400:
		if rng.Float64() < 0.6 {
			plan.ctype = contentTypeIndex("application/json")
		} else {
			plan.ctype = contentTypeIndex("text/html")
		}
		plan.respSize = logNormal(rng, 300, 0.8, 20, 64<<10)
		plan.path = p.path(plan.ctype, host)
	default:
		plan.ctype = contentTypeWeights.pick(rng)
		if plan.status != 204 {
			plan.respSize = logNormal(rng, 1500, 2.34, 16, 4<<20)
		}
		plan.path = p.path(plan.ctype, host)
		if contentTypes[plan.ctype].kind != bodyBinary && plan.respSize > 0 {
			switch r := rng.Float64() * 100; {
			case r < 1:
				plan.codings = []string{"deflate"}
			case r < 1.5:
				plan.codings = []string{"zstd"}
			case r < 1.8:
				plan.codings = []string{"deflate", "gzip"}
			case r < 2.3:
				// Chunked gzip has no Content-Length, so the proxy stores it encoded.
				plan.codings = []string{"gzip"}
				plan.chunked = true
			}
		}
	}
}

// path returns a path fitting the content type, depth 1 to 12 with a mean
// of 3 to 4.
func (p *planner) path(ctype int, host int32) string {
	rng := p.rng
	w := func() string { return word(rng) }
	var path string
	switch contentTypes[ctype].group {
	case "json", "other", "text":
		switch r := rng.Float64(); {
		case r < 0.15 && (host == 0 || host == 6):
			path = "/graphql"
		case r < 0.30 && host == 6:
			path = "/api/graphql/" + w()
		case r < 0.45:
			path = "/api/v2/users/" + itoa(1+rng.IntN(50_000))
		case r < 0.55:
			path = "/api/v2/orders/" + uuidString(rng)
		case r < 0.75:
			path = "/api/v1/" + w() + "/" + itoa(rng.IntN(5000))
		case r < 0.9:
			path = "/api/v1/" + w() + "/" + itoa(rng.IntN(500)) + "/" + w()
		default:
			path = "/api/v3/" + w() + "/" + w() + "/" + w() + "/" + itoa(rng.IntN(100)) + "/" + w()
		}
	case "html":
		switch r := rng.Float64(); {
		case r < 0.2:
			path = "/"
		case r < 0.5:
			path = "/" + w()
		case r < 0.8:
			path = "/" + w() + "/" + w()
		default:
			path = "/docs/" + w() + "/" + w() + "/" + w() + ".html"
		}
	case "js":
		path = "/static/js/" + w() + "." + hexString(rng, 8) + ".js"
	case "css":
		path = "/static/css/" + w() + "." + hexString(rng, 8) + ".css"
	case "image":
		path = "/assets/img/" + hexString(rng, 16) + ".img"
	case "font":
		path = "/fonts/" + w() + ".font"
	default:
		path = "/" + w() + "/download/" + itoa(rng.IntN(100_000))
	}
	// A tenth of paths sit under a deeper prefix.
	if depth := strings.Count(path, "/"); rng.Float64() < 0.1 && depth < 12 {
		prefix := ""
		for range 1 + rng.IntN(min(7, 12-depth)) {
			prefix += "/" + w()
		}
		path = prefix + path
	}
	if rng.Float64() < 0.2 {
		path += "?" + w() + "=" + w() + "&page=" + itoa(rng.IntN(20))
	}
	return path
}
