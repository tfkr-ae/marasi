package marasi

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/internal/h1"
)

// newScriptedH1Origin is an origin that runs script on each connection it
// accepts, so a test decides when each byte goes out.
func newScriptedH1Origin(t *testing.T, script func(conn net.Conn, br *bufio.Reader)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the origin: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(15 * time.Second))
				script(conn, bufio.NewReader(conn))
			}()
		}
	}()
	return l.Addr().String()
}

// withBodyThreshold sets the body threshold to n bytes.
func withBodyThreshold(n int64) func(*Proxy) {
	return func(proxy *Proxy) { proxy.Config.BodyThreshold = n }
}

// readSome reads exactly n bytes from r, failing the test after wait.
func readSome(t *testing.T, conn net.Conn, r io.Reader, n int, wait time.Duration) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(wait))
	defer conn.SetReadDeadline(time.Time{})
	got := make([]byte, n)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("reading %d bytes: %v (got %q)", n, err, got)
	}
	return string(got)
}

// expectPassedThrough checks the stored message's raw text and that its
// metadata names the direction as passed through, too large.
func expectPassedThrough(t *testing.T, direction, raw string, gotRaw []byte, metadata map[string]any) {
	t.Helper()
	if string(gotRaw) != raw {
		t.Fatalf("wanted the stored %s:\n%q\ngot:\n%q", direction, raw, gotRaw)
	}
	if metadata[direction+"_body_ending"] != "too large" || metadata[direction+"_passed_through"] != true {
		t.Fatalf("wanted the %s marked passed through and too large in metadata\ngot:\n%v", direction, metadata)
	}
}

func TestH1Engine_BodyThreshold(t *testing.T) {
	t.Run("should send a request declaring a length over the threshold head first and give the client the origin's early answer", func(t *testing.T) {
		heads := make(chan string, 1)
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			head, err := h1.ReadRequestHead(br)
			if err != nil {
				return
			}
			heads <- string(head.Bytes())
			io.WriteString(conn, "HTTP/1.1 413 Request Entity Too Large\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			// As nginx does, it lingers before it closes.
			time.Sleep(time.Second)
		})
		_, addr, captured := serveFramingProxy(t, nil)

		conn, br := dialProxy(t, addr)
		host := "Host: " + origin + "\r\n"
		io.WriteString(conn, "POST http://"+origin+"/ HTTP/1.1\r\n"+host+"Content-Length: 999999999\r\n\r\n")
		readExactly(t, br, "HTTP/1.1 413 Request Entity Too Large\r\nContent-Length: 0\r\n\r\n")

		want := "POST / HTTP/1.1\r\n" + host + "Content-Length: 999999999\r\n\r\n"
		select {
		case got := <-heads:
			if got != want {
				t.Fatalf("wanted the origin to receive:\n%q\ngot:\n%q", want, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the origin received no request head")
		}
		select {
		case req := <-captured.request:
			expectPassedThrough(t, "request", want, req.Raw, req.Metadata)
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		// The client connection ends when the origin's does, as going
		// direct.
		expectOpen(t, conn, br, 500*time.Millisecond)
		expectClosed(t, conn, br, 5*time.Second)
	})

	t.Run("should relay the origin's 100 Continue to a request declaring a length over the threshold", func(t *testing.T) {
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			io.WriteString(conn, "HTTP/1.1 100 Continue\r\n\r\n")
			if readSome(t, conn, br, 10, 5*time.Second) == "0123456789" {
				io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
			}
		})
		_, addr, _ := serveFramingProxy(t, withBodyThreshold(4))

		conn, br := dialProxy(t, addr)
		io.WriteString(conn, "POST http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\nExpect: 100-continue\r\nContent-Length: 10\r\n\r\n")
		readExactly(t, br, "HTTP/1.1 100 Continue\r\n\r\n")
		io.WriteString(conn, "0123456789")
		readExactly(t, br, "HTTP/1.1 204 No Content\r\n\r\n")
	})

	t.Run("should forward a request body from the point it passes the threshold, as it arrives, and store it up to the threshold", func(t *testing.T) {
		arrived := make(chan string, 2)
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			arrived <- readSome(t, conn, br, 15, 5*time.Second)
			arrived <- readSome(t, conn, br, 5, 5*time.Second)
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		})
		_, addr, captured := serveFramingProxy(t, withBodyThreshold(8))

		conn, br := dialProxy(t, addr)
		host := "Host: " + origin + "\r\n"
		io.WriteString(conn, "POST http://"+origin+"/ HTTP/1.1\r\n"+host+"Transfer-Encoding: chunked\r\n\r\na\r\n0123456789\r\n")
		select {
		case got := <-arrived:
			if got != "a\r\n0123456789\r\n" {
				t.Fatalf("wanted the origin to receive what arrived\ngot:\n%q", got)
			}
		case <-time.After(time.Second):
			t.Fatal("the origin did not receive the body that passed the threshold before the client finished it")
		}
		io.WriteString(conn, "0\r\n\r\n")
		if got := <-arrived; got != "0\r\n\r\n" {
			t.Fatalf("wanted the origin to receive the rest\ngot:\n%q", got)
		}
		readExactly(t, br, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")

		req := <-captured.request
		expectPassedThrough(t, "request", "POST / HTTP/1.1\r\n"+host+"Transfer-Encoding: chunked\r\n\r\n01234567", req.Raw, req.Metadata)
	})

	t.Run("should forward a request body that stalls after some arrived, and keep the connection for the next request", func(t *testing.T) {
		arrived := make(chan string, 2)
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			for {
				head, err := h1.ReadRequestHead(br)
				if err != nil {
					return
				}
				if head.Method == "POST" {
					arrived <- readSome(t, conn, br, 5, 5*time.Second)
					arrived <- readSome(t, conn, br, 6, 5*time.Second)
				}
				io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}
		})
		_, addr, _ := serveFramingProxy(t, nil)

		conn, br := dialProxy(t, addr)
		host := "Host: " + origin + "\r\n"
		io.WriteString(conn, "POST http://"+origin+"/ HTTP/1.1\r\n"+host+"Transfer-Encoding: chunked\r\n\r\n1\r\na\r\n")
		select {
		case got := <-arrived:
			if got != "1\r\na\r" {
				t.Fatalf("wanted the origin to receive what arrived as framed\ngot:\n%q", got)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("the origin did not receive the stalled body")
		}
		io.WriteString(conn, "0\r\n\r\n")
		if got := <-arrived; got != "\n0\r\n\r\n" {
			t.Fatalf("wanted the origin to receive the rest as framed\ngot:\n%q", got)
		}
		readExactly(t, br, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\n"+host+"\r\n")
		readExactly(t, br, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	})

	t.Run("should send the rest of a passed request body to an origin that answered first and keeps its connection, then carry the next request", func(t *testing.T) {
		rest := make(chan string, 1)
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			readSome(t, conn, br, 15, 5*time.Second)
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nearly")
			rest <- readSome(t, conn, br, 5, 5*time.Second)
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
		})
		_, addr, _ := serveFramingProxy(t, withBodyThreshold(8))

		conn, br := dialProxy(t, addr)
		host := "Host: " + origin + "\r\n"
		io.WriteString(conn, "POST http://"+origin+"/ HTTP/1.1\r\n"+host+"Transfer-Encoding: chunked\r\n\r\na\r\n0123456789\r\n")
		readExactly(t, br, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nearly")
		io.WriteString(conn, "0\r\n\r\n")
		if got := <-rest; got != "0\r\n\r\n" {
			t.Fatalf("wanted the origin to receive the rest of the body\ngot:\n%q", got)
		}
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\n"+host+"\r\n")
		readExactly(t, br, "HTTP/1.1 204 No Content\r\n\r\n")
	})

	t.Run("should forward a response body from the point it passes the threshold, as it arrives, and store it up to the threshold", func(t *testing.T) {
		more := make(chan struct{})
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\na\r\n0123456789\r\n")
			<-more
			io.WriteString(conn, "0\r\n\r\n")
		})
		_, addr, captured := serveFramingProxy(t, withBodyThreshold(8))

		conn, br := dialProxy(t, addr)
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
		const head = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"
		if got := readSome(t, conn, br, len(head)+15, time.Second); got != head+"a\r\n0123456789\r\n" {
			t.Fatalf("wanted the client to receive what arrived\ngot:\n%q", got)
		}
		close(more)
		readExactly(t, br, "0\r\n\r\n")

		res := <-captured.response
		expectPassedThrough(t, "response", head+"01234567", res.Raw, res.Metadata)
	})

	t.Run("should forward a chunked response body that stalls after some arrived, and keep the connection for the next request", func(t *testing.T) {
		more := make(chan struct{})
		origin := newScriptedH1Origin(t, func(conn net.Conn, br *bufio.Reader) {
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			io.WriteString(conn, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nfirst\r\n")
			<-more
			io.WriteString(conn, "6\r\nsecond\r\n0\r\n\r\n")
			if _, err := h1.ReadRequestHead(br); err != nil {
				return
			}
			io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
		})
		_, addr, _ := serveFramingProxy(t, nil)

		conn, br := dialProxy(t, addr)
		host := "Host: " + origin + "\r\n"
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\n"+host+"\r\n")
		const head = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"
		if got := readSome(t, conn, br, len(head)+10, 4*time.Second); got != head+"5\r\nfirst\r\n" {
			t.Fatalf("wanted the client to receive what arrived\ngot:\n%q", got)
		}
		close(more)
		readExactly(t, br, "6\r\nsecond\r\n0\r\n\r\n")
		io.WriteString(conn, "GET http://"+origin+"/ HTTP/1.1\r\n"+host+"\r\n")
		readExactly(t, br, "HTTP/1.1 204 No Content\r\n\r\n")
	})

	t.Run("should neither hold nor edit a passed pair, and name each skipped hold and edit in a proxy log", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n0123456789")
		logs := make(chan string, 64)
		proxy, addr, _ := serveFramingProxy(t, func(proxy *Proxy) {
			withBodyThreshold(4)(proxy)
			collectLogs(logs)(proxy)
			ext := &domain.Extension{Name: "workshop", ID: testExtensions["workshop"].ID, Enabled: true, LuaContent: `
				function processRequest(request) request:set_body("edited") end
				function processResponse(response) response:set_body("edited") end
			`}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatalf("adding the extension: %v", err)
			}
			proxy.AddRequestPairModifier(ExtensionsRequestModifier)
			proxy.AddResponsePairModifier(ExtensionsResponseModifier)
			holdEverything(t)(proxy)
		})

		host := "Host: " + origin.addr + "\r\n"
		roundTripRaw(t, addr, "POST http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"Content-Length: 10\r\n\r\n0123456789",
			"HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n0123456789")
		if want := "POST / HTTP/1.1\r\n" + host + "Content-Length: 10\r\n\r\n0123456789"; origin.request(t) != want {
			t.Fatalf("wanted the origin to receive the request unedited:\n%q", want)
		}
		if proxy.HasPendingCheckpoint() {
			t.Fatal("wanted nothing held in Checkpoint")
		}
		expectLog(t, logs, "Lua body edit", "request to POST")
		expectLog(t, logs, "Checkpoint hold", "request to POST")
		expectLog(t, logs, "Lua body edit", "response to POST")
		expectLog(t, logs, "Checkpoint hold", "response to POST")
	})

	t.Run("should link each skipped hold and edit's proxy log to the stored request", func(t *testing.T) {
		origin := newBodyOrigin(t, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n0123456789")
		proxy, addr, captured := serveFramingProxy(t, func(proxy *Proxy) {
			withBodyThreshold(4)(proxy)
			ext := &domain.Extension{Name: "workshop", ID: testExtensions["workshop"].ID, Enabled: true, LuaContent: `
				function processRequest(request) request:set_body("edited") end
				function processResponse(response) response:set_body("edited") end
			`}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatalf("adding the extension: %v", err)
			}
			proxy.AddRequestPairModifier(ExtensionsRequestModifier)
			proxy.AddResponsePairModifier(ExtensionsResponseModifier)
			holdEverything(t)(proxy)
		})

		host := "Host: " + origin.addr + "\r\n"
		roundTripRaw(t, addr, "POST http://"+origin.addr+"/ HTTP/1.1\r\n"+host+"Content-Length: 10\r\n\r\n0123456789",
			"HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n0123456789")
		var requestID string
		select {
		case req := <-captured.request:
			requestID = req.ID.String()
		case <-time.After(5 * time.Second):
			t.Fatal("the request was not captured")
		}
		select {
		case <-captured.response:
		case <-time.After(5 * time.Second):
			t.Fatal("the response was not captured")
		}

		// A log whose request link fails its foreign key is never stored,
		// so read them back from the database.
		want := map[string]bool{
			"Lua body edit|request": false, "Checkpoint hold|request": false,
			"Lua body edit|response": false, "Checkpoint hold|response": false,
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			stored, err := proxy.LogRepo.GetLogs()
			if err != nil {
				t.Fatalf("reading the proxy logs: %v", err)
			}
			for _, entry := range stored {
				for key := range want {
					skipped, direction, _ := strings.Cut(key, "|")
					if strings.Contains(entry.Message, skipped) && strings.Contains(entry.Message, direction+" to POST") &&
						entry.RequestID != nil && entry.RequestID.String() == requestID {
						want[key] = true
					}
				}
			}
			missing := []string{}
			for key, found := range want {
				if !found {
					missing = append(missing, key)
				}
			}
			if len(missing) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("wanted these proxy logs stored with request_id %s: %v\ngot:\n%v", requestID, missing, logSummaries(stored))
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// logSummaries prints each stored log's message and request link.
func logSummaries(logs []*domain.Log) string {
	var b strings.Builder
	for _, entry := range logs {
		link := "<nil>"
		if entry.RequestID != nil {
			link = entry.RequestID.String()
		}
		b.WriteString(entry.Level + " request_id=" + link + " " + entry.Message + "\n")
	}
	return b.String()
}

// collectLogs gathers the proxy's log messages.
func collectLogs(logs chan<- string) func(*Proxy) {
	return func(proxy *Proxy) {
		proxy.OnLog = func(log domain.Log) error {
			select {
			case logs <- log.Message:
			default:
			}
			return nil
		}
	}
}

// expectLog waits for a log message containing each of parts.
func expectLog(t *testing.T, logs <-chan string, parts ...string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case message := <-logs:
			matched := true
			for _, part := range parts {
				matched = matched && strings.Contains(message, part)
			}
			if matched {
				return
			}
		case <-deadline:
			t.Fatalf("wanted a proxy log naming %q", parts)
		}
	}
}
