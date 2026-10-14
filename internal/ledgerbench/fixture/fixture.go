// Package fixture builds the reference project the ledger benchmarks run
// against: a .marasi project of request/response pairs whose shape follows a
// real engagement (see the ledger-redesign-support spec, ticket 01).
//
// Build writes every pair through the real migrations and the real capture
// path: InsertRequest, then InsertResponse, with about 50 pairs in flight, and
// then runs the real traffic index build. Bulk-inserting complete rows would
// give an unrealistically compact layout. The same seed and size always give
// the same pairs, ids, bodies, notes and links.
//
// Next to the project, Build writes a manifest (path + ".manifest.json")
// naming the planted facts benchmarks query for, such as the sparse hosts,
// the rare text terms and the excluded stretches.
package fixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/rawhttp"
)

// Reference sizes.
const (
	ReferencePairs = 1_000_000 // the reference project of the spec
	IterationPairs = 100_000   // a smaller project for iterating
)

// End is when the newest pair of every fixture was requested.
var End = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// inFlight is how many pairs are waiting for their response at a time.
const inFlight = 50

// Config sets what Build writes.
type Config struct {
	Seed  uint64
	Pairs int
	// Progress, when set, is called as the build goes with a stage name and
	// how much of it is done.
	Progress func(stage string, done, total int)
}

// Manifest names the facts a built project was planted with.
type Manifest struct {
	Seed   uint64    `json:"seed"`
	Pairs  int       `json:"pairs"`
	Oldest time.Time `json:"oldest"`
	Newest time.Time `json:"newest"`
	// MiddleID is the id of the pair halfway back, a deep cursor.
	MiddleID uuid.UUID `json:"middle_id"`

	// DominantHost carries about 25% of pairs.
	DominantHost string `json:"dominant_host"`
	// DenseHostPattern matches the top hosts, as in host = "*.example.com".
	DenseHostPattern string `json:"dense_host_pattern"`
	// SparseHostPattern matches a few tail hosts.
	SparseHostPattern string `json:"sparse_host_pattern"`
	// SpreadHost matches pairs spread evenly over time, so its 201st newest
	// match is about halfway back.
	SpreadHost      string `json:"spread_host"`
	SpreadHostPairs int    `json:"spread_host_pairs"`
	// OldHost and RareOldHost match only pairs in the oldest 45%.
	// RareOldHost has fewer than a page of matches.
	OldHost          string `json:"old_host"`
	OldHostPairs     int    `json:"old_host_pairs"`
	RareOldHost      string `json:"rare_old_host"`
	RareOldHostPairs int    `json:"rare_old_host_pairs"`
	// GraphQLPattern matches the dense GraphQL endpoints.
	GraphQLPattern string `json:"graphql_pattern"`

	// RareTerm is in the response bodies of RareTermPairs pairs, spread over time.
	RareTerm      string `json:"rare_term"`
	RareTermPairs int    `json:"rare_term_pairs"`
	// RareNoteTerm is in one note, on an old pair.
	RareNoteTerm string `json:"rare_note_term"`
	// CommonTerm and CommonPhrase are in a large share of pairs.
	CommonTerm   string `json:"common_term"`
	CommonPhrase string `json:"common_phrase"`
	// RareExtension is a rare value of metadata.extension.
	RareExtension string `json:"rare_extension"`

	// BigLaunchpad has 1% of pairs; OldLaunchpad has 100 pairs, all old.
	BigLaunchpad uuid.UUID `json:"big_launchpad"`
	OldLaunchpad uuid.UUID `json:"old_launchpad"`
	// SmallFinding has 50 pairs; LargeTestCase has the most pairs of any
	// test case (1,000 at the reference size).
	SmallFinding       uuid.UUID `json:"small_finding"`
	LargeTestCase      uuid.UUID `json:"large_test_case"`
	LargeTestCasePairs int       `json:"large_test_case_pairs"`

	// ExcludedStretches are runs of pairs whose content types are all in the
	// seeded exclusion: 1% and 10% of pairs (10k and 100k at the reference
	// size). Listing from cursor Above starts at the stretch's newest pair.
	ExcludedStretches []ExcludedStretch `json:"excluded_stretches"`
}

// ExcludedStretch is a run of pairs of excluded content types.
type ExcludedStretch struct {
	Pairs int       `json:"pairs"`
	Above uuid.UUID `json:"above"`
}

// ManifestPath is where Build writes the manifest of the project at path.
func ManifestPath(path string) string { return path + ".manifest.json" }

// ReadManifest reads the manifest of the project at path.
func ReadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(ManifestPath(path))
	if err != nil {
		return nil, fmt.Errorf("reading fixture manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decoding fixture manifest: %w", err)
	}
	return &manifest, nil
}

// Build writes a new project at path and its manifest. path must not exist.
func Build(path string, cfg Config) (*Manifest, error) {
	if cfg.Pairs < 100 {
		return nil, errors.New("a fixture needs at least 100 pairs")
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", path)
	}
	progress := cfg.Progress
	if progress == nil {
		progress = func(string, int, int) {}
	}

	conn, err := db.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, err
	}
	repo := db.NewProxyRepo(conn)
	defer repo.Close()

	plans, hosts, layout := planPairs(cfg.Seed, cfg.Pairs, End)
	b := &builder{repo: repo, seed: cfg.Seed, plans: plans, hosts: hosts, layout: layout, progress: progress}
	manifest, err := b.build()
	if err != nil {
		return nil, err
	}

	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(ManifestPath(path), raw, 0o644); err != nil {
		return nil, fmt.Errorf("writing fixture manifest: %w", err)
	}
	return manifest, nil
}

type builder struct {
	repo     *db.Repository
	seed     uint64
	plans    []pairPlan
	hosts    hostTable
	layout   layout
	progress func(string, int, int)
	runs     []uuid.UUID
}

// captured is one pair as the proxy would hand it to the repository.
type captured struct {
	request  *domain.ProxyRequest
	response *domain.ProxyResponse
}

func (b *builder) build() (*Manifest, error) {
	if err := b.createArmoryRuns(); err != nil {
		return nil, err
	}
	if err := b.capture(); err != nil {
		return nil, err
	}
	manifest := b.manifest()
	if err := b.annotate(manifest); err != nil {
		return nil, err
	}
	if err := b.indexBuild(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// pairRNG is the random stream of one pair's content, so content can be
// generated in parallel and still repeat exactly.
func (b *builder) pairRNG(i int) *rand.Rand {
	return rand.New(rand.NewPCG(b.seed, uint64(i)+0x10000))
}

func (b *builder) createArmoryRuns() error {
	clock := newIDClock(rand.New(rand.NewPCG(b.seed, 0xa7)))
	for n, burst := range b.layout.bursts {
		started := b.plans[burst.start].at
		finished := b.plans[burst.start+burst.length-1].at
		template := &domain.ArmoryTemplate{
			ID:          clock.at(started.Add(-time.Hour)),
			Name:        fmt.Sprintf("Fuzz %d", n+1),
			RawTemplate: "POST " + b.plans[burst.start].path + " HTTP/1.1\r\nHost: {{host}}\r\n\r\n{{payload}}",
		}
		if err := b.repo.CreateArmoryTemplate(template); err != nil {
			return err
		}
		run := &domain.ArmoryRun{
			ID: clock.at(started.Add(-time.Minute)), TemplateID: template.ID, TemplateSnapshot: template.RawTemplate,
			UseHTTPS: true, Wordlists: []string{"passwords.txt"}, Status: domain.ArmoryRunCompleted,
			AttackType: domain.ArmoryAttackHarpoon, MaxConcurrent: 20, CreatedAt: started.Add(-time.Minute),
			StartedAt: &started, FinishedAt: &finished,
		}
		if err := b.repo.CreateArmoryRun(run); err != nil {
			return err
		}
		b.runs = append(b.runs, run.ID)
	}
	return nil
}

// capture writes every pair through InsertRequest and InsertResponse, with
// about 50 requests waiting for their response at a time, as the proxy's
// database writer does. Content is generated in parallel ahead of the writer.
func (b *builder) capture() error {
	const chunk = 256
	logs := newIDClock(rand.New(rand.NewPCG(b.seed, 0x10c)))
	order := rand.New(rand.NewPCG(b.seed, 0x0de7))
	var waiting []int
	pending := map[int]*captured{}

	respond := func(i int) error {
		pair := pending[i]
		delete(pending, i)
		plan := &b.plans[i]
		if pair.response != nil {
			if err := b.repo.InsertResponse(pair.response); err != nil {
				return err
			}
			if plan.websocket {
				if err := b.websocket(i); err != nil {
					return err
				}
			}
		}
		at := plan.at.Add(plan.latency)
		return b.repo.InsertLog(&domain.Log{
			ID: logs.at(plan.at), Timestamp: at, Level: "INFO",
			Message:   fmt.Sprintf("%s %s %d", plan.method, b.hosts.names[plan.host], plan.status),
			Context:   map[string]any{"component": "proxy"},
			RequestID: &plan.id,
		})
	}

	for start := 0; start < len(b.plans); start += chunk {
		end := min(start+chunk, len(b.plans))
		batch := b.generate(start, end)
		for k, pair := range batch {
			i := start + k
			if err := b.repo.InsertRequest(pair.request); err != nil {
				return err
			}
			if run := b.plans[i].burst; run > 0 {
				if err := b.repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: b.runs[run-1], RequestID: pair.request.ID}); err != nil {
					return err
				}
			}
			pending[i] = pair
			waiting = append(waiting, i)
			if len(waiting) >= inFlight {
				n := order.IntN(len(waiting))
				next := waiting[n]
				waiting = append(waiting[:n], waiting[n+1:]...)
				if err := respond(next); err != nil {
					return err
				}
			}
		}
		b.progress("capture", end, len(b.plans))
	}
	for _, i := range waiting {
		if err := respond(i); err != nil {
			return err
		}
	}
	return nil
}

// generate builds the captured form of pairs [start, end) in parallel.
func (b *builder) generate(start, end int) []*captured {
	out := make([]*captured, end-start)
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				out[i-start] = b.capturedPair(i)
			}
		})
	}
	for i := start; i < end; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

var userAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
	"okhttp/4.12.0",
}

func (b *builder) capturedPair(i int) *captured {
	rng := b.pairRNG(i)
	plan := &b.plans[i]
	host := b.hosts.names[plan.host]
	scheme := "https"
	if !plan.https {
		scheme = "http"
	}

	metadata := map[string]any{}
	if plan.extension != "" {
		metadata["extension"] = plan.extension
	}

	// Request.
	var reqBody []byte
	reqType := ""
	if plan.reqBodySize > 0 {
		if plan.burst > 0 || rng.Float64() < 0.7 {
			reqType = "application/json"
			reqBody = textBody(rng, bodyJSON, plan.reqBodySize, "")
		} else {
			reqType = "application/x-www-form-urlencoded"
			var form strings.Builder
			for form.Len() < plan.reqBodySize {
				if form.Len() > 0 {
					form.WriteByte('&')
				}
				form.WriteString(word(rng) + "=" + strings.ReplaceAll(sentence(rng, 3), " ", "+"))
			}
			reqBody = []byte(form.String())
		}
	}
	var head strings.Builder
	fmt.Fprintf(&head, "%s %s HTTP/1.1\r\nHost: %s\r\n", plan.method, plan.path, host)
	if plan.websocket {
		head.WriteString("Connection: Upgrade\r\nUpgrade: websocket\r\nSec-Websocket-Version: 13\r\nSec-Websocket-Key: " + hexString(rng, 22) + "==\r\n")
	}
	fmt.Fprintf(&head, "User-Agent: %s\r\nAccept: */*\r\nAccept-Encoding: gzip, deflate, br, zstd\r\n", userAgents[rng.IntN(len(userAgents))])
	if rng.Float64() < 0.6 {
		fmt.Fprintf(&head, "Cookie: session=%s; theme=%s\r\n", hexString(rng, 32), word(rng))
	}
	if rng.Float64() < 0.3 {
		fmt.Fprintf(&head, "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.%s.%s\r\n", hexString(rng, 48), hexString(rng, 32))
	}
	if reqBody != nil {
		fmt.Fprintf(&head, "Content-Type: %s\r\nContent-Length: %d\r\n", reqType, len(reqBody))
	}
	head.WriteString("\r\n")
	requestRaw := append([]byte(head.String()), reqBody...)
	if pretty, err := rawhttp.Prettify(reqBody); err == nil && len(pretty) > 0 {
		metadata["prettified-request"] = head.String() + string(pretty)
	}
	request := &domain.ProxyRequest{
		ID: plan.id, Scheme: scheme, Method: plan.method, Host: host, Path: plan.path,
		Raw: requestRaw, Metadata: maps.Clone(metadata), RequestedAt: plan.at,
	}
	if plan.status < 0 {
		return &captured{request: request}
	}

	// Response.
	ct := contentTypes[plan.ctype]
	var body []byte
	switch {
	case plan.respSize == 0:
	case ct.kind == bodyBinary:
		body = binaryBody(rng, ct.magic, plan.respSize)
	default:
		plant := ""
		if plan.plantRare {
			plant = rareTerm
		}
		body = textBody(rng, ct.kind, plan.respSize, plant)
	}
	var pretty []byte
	if plan.codings == nil && len(body) > 0 && ct.kind != bodyBinary {
		pretty, _ = rawhttp.Prettify(body)
	}
	stored := body
	if plan.codings != nil {
		stored = encodeBody(body, plan.codings)
	}

	statusText := fmt.Sprintf("%d %s", plan.status, statusTexts[plan.status])
	head.Reset()
	fmt.Fprintf(&head, "HTTP/1.1 %s\r\n", statusText)
	length := ""
	if plan.chunked {
		head.WriteString("Transfer-Encoding: chunked\r\n")
	} else if plan.status != 101 {
		length = itoa(len(stored))
		fmt.Fprintf(&head, "Content-Length: %s\r\n", length)
	}
	if len(plan.codings) > 0 {
		fmt.Fprintf(&head, "Content-Encoding: %s\r\n", strings.Join(plan.codings, ", "))
	}
	if plan.status == 101 {
		head.WriteString("Connection: Upgrade\r\nUpgrade: websocket\r\nSec-Websocket-Accept: " + hexString(rng, 27) + "=\r\n")
	} else {
		typeHeader := ct.name
		if ct.kind != bodyBinary && rng.Float64() < 0.5 {
			typeHeader += "; charset=utf-8"
		}
		fmt.Fprintf(&head, "Content-Type: %s\r\n", typeHeader)
	}
	if plan.status >= 300 && plan.status < 400 && plan.status != 304 {
		fmt.Fprintf(&head, "Location: /%s/%s\r\n", word(rng), word(rng))
	}
	fmt.Fprintf(&head, "Date: %s\r\nServer: %s\r\nCache-Control: %s\r\nX-Request-Id: %s\r\n",
		plan.at.Add(plan.latency).Format(time.RFC1123), []string{"nginx", "cloudflare", "envoy", "AmazonS3"}[rng.IntN(4)],
		[]string{"no-store", "max-age=3600", "private, max-age=0", "public, max-age=31536000"}[rng.IntN(4)], uuidString(rng))
	if rng.Float64() < 0.1 {
		fmt.Fprintf(&head, "Set-Cookie: %s=%s; Path=/; HttpOnly; Secure\r\n", word(rng), hexString(rng, 24))
	}
	head.WriteString("\r\n")
	responseRaw := append([]byte(head.String()), stored...)

	responseMetadata := maps.Clone(metadata)
	if len(pretty) > 0 {
		responseMetadata["prettified-response"] = head.String() + string(pretty)
	}
	responseType := ct.name
	return &captured{
		request: request,
		response: &domain.ProxyResponse{
			ID: plan.id, Status: statusText, StatusCode: plan.status, ContentType: responseType,
			Length: length, Raw: responseRaw, Metadata: responseMetadata, RespondedAt: plan.at.Add(plan.latency),
		},
	}
}

var statusTexts = map[int]string{
	101: "Switching Protocols", 200: "OK", 201: "Created", 204: "No Content", 206: "Partial Content",
	301: "Moved Permanently", 302: "Found", 304: "Not Modified", 400: "Bad Request", 401: "Unauthorized",
	403: "Forbidden", 404: "Not Found", 429: "Too Many Requests", 500: "Internal Server Error",
	502: "Bad Gateway", 503: "Service Unavailable", 504: "Gateway Timeout",
}

// websocket records a connection with messages for an upgraded pair.
func (b *builder) websocket(i int) error {
	plan := &b.plans[i]
	rng := rand.New(rand.NewPCG(b.seed, uint64(i)+0x3500000))
	clock := newIDClock(rng)
	opened := plan.at.Add(plan.latency)
	conn := &domain.WebSocketConnection{
		ID: clock.at(opened), RequestID: plan.id, State: "closed", Transport: "wss",
		Host: b.hosts.names[plan.host], Path: plan.path, StartedAt: opened, CloseCode: 1000,
	}
	messages := 50 + rng.IntN(450)
	closed := opened.Add(time.Duration(messages) * time.Second)
	conn.ClosedAt = &closed
	if err := b.repo.InsertConnection(conn); err != nil {
		return err
	}
	for m := range messages {
		at := opened.Add(time.Duration(m) * time.Second)
		direction := []string{"client", "server"}[rng.IntN(2)]
		payload := fmt.Sprintf(`{"type":%q,"seq":%d,"body":%q}`, word(rng), m, sentence(rng, 3+rng.IntN(20)))
		err := b.repo.InsertMessage(&domain.WebSocketMessage{
			ID: clock.at(at), ConnectionID: conn.ID, RequestID: plan.id, Direction: direction,
			Opcode: 1, FrameCount: 1, Payload: []byte(payload), CreatedAt: at, Metadata: map[string]any{},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// indexBuild runs the real traffic index build until nothing is missing.
func (b *builder) indexBuild() error {
	for {
		_, remaining, err := b.repo.IndexMissingTraffic(1 << 20)
		var skipped *db.SkippedPairError
		if errors.As(err, &skipped) {
			continue
		}
		if err != nil {
			return err
		}
		if !remaining {
			return nil
		}
	}
}

func (b *builder) manifest() *Manifest {
	n := len(b.plans)
	m := &Manifest{
		Seed: b.seed, Pairs: n,
		Oldest: b.plans[0].at, Newest: b.plans[n-1].at, MiddleID: b.plans[n/2].id,
		DominantHost: topHosts[0], DenseHostPattern: "*.example.com", SparseHostPattern: "*" + sparseHostSuffix,
		SpreadHost: spreadHost, SpreadHostPairs: b.layout.spreadPairs,
		OldHost: oldHost, OldHostPairs: b.layout.oldPairs, RareOldHost: rareOldHost, RareOldHostPairs: b.layout.rareOldPairs,
		GraphQLPattern: "*graphql*",
		RareTerm:       rareTerm, RareTermPairs: rareTermPairs, RareNoteTerm: rareNoteTerm,
		CommonTerm: "son", CommonPhrase: "content-type: application/json",
		RareExtension: rareExtension,
	}
	for _, s := range b.layout.excluded {
		m.ExcludedStretches = append(m.ExcludedStretches, ExcludedStretch{Pairs: s.length, Above: b.plans[s.start+s.length].id})
	}
	return m
}
