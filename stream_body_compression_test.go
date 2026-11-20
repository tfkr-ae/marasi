package marasi

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gzipStream is a sender's gzip compressor over one body, flushed after
// each stream message so each can be read as it arrives.
type gzipStream struct {
	buf bytes.Buffer
	w   *gzip.Writer
}

func newGzipStream() *gzipStream {
	s := &gzipStream{}
	s.w = gzip.NewWriter(&s.buf)
	// A name in the header makes the sender's bytes differ from any
	// compressor that writes none, as Marasi's does.
	s.w.Name = "origin"
	return s
}

// message is the bytes that carry text, flushed.
func (s *gzipStream) message(text string) string {
	s.w.Write([]byte(text))
	s.w.Flush()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

// end is the bytes that end the compressed stream.
func (s *gzipStream) end() string {
	s.w.Close()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

// inflatedSoFar is what the gzip bytes received so far inflate to, and
// whether the compressed stream ended.
func inflatedSoFar(data []byte) (string, bool) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", false
	}
	out, err := io.ReadAll(r)
	return string(out), err == nil
}

func TestH2Engine_CompressedEventStream(t *testing.T) {
	t.Run("should inflate a gzipped event stream, send each event on to the client as it arrives, compressed again, and store each event decoded", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
		id := upstream.nthStream(t, 0)
		gz := newGzipStream()
		upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "gzip")
		upstream.data(id, false, gz.message("id: 1\ndata: first\n\n"))
		client.await(t, "the first event", func(p *h2TestPeer) bool {
			s := p.streams[1]
			if s == nil {
				return false
			}
			got, _ := inflatedSoFar(s.data)
			return got == "id: 1\ndata: first\n\n"
		})

		upstream.data(id, false, gz.message("id: 2\ndata: second\n\n"))
		upstream.data(id, true, gz.end())
		got := client.stream(t, 1)
		if body, ended := inflatedSoFar(got.data); body != "id: 1\ndata: first\n\nid: 2\ndata: second\n\n" || !ended {
			t.Fatalf("the client's body, inflated\nwanted:\nboth events and the end of the gzip stream\ngot:\n%q (ended %v)", body, ended)
		}
		if encoding, _ := got.headers[0].get("content-encoding"); encoding != "gzip" {
			t.Errorf("the client's content-encoding\nwanted:\ngzip\ngot:\n%q", encoding)
		}

		req, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "response"); d["framing"] != "sse" || d["messages"] != float64(2) || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.response\nwanted:\nsse, 2 messages, complete\ngot:\n%v", d)
		}
		messages := storedMessages(t, proxyOf(t, addr), req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "id: 1\ndata: first\n\n" || string(messages[1].Payload) != "id: 2\ndata: second\n\n" || messages[0].Compressed {
			t.Fatalf("stored messages\nwanted:\nthe two events, decoded, not marked compressed\ngot:\n%+v", messages)
		}
	})

	t.Run("should show a hook each event decoded and send its edit to the client compressed again", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, withStreamExtension(t, "rewriter", `
			function processStreamMessage(message)
				if message:payload() == "data: first\n\n" then
					message:set_payload("data: edited\n\n")
				end
			end
		`))
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
		id := upstream.nthStream(t, 0)
		gz := newGzipStream()
		upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "gzip")
		upstream.data(id, false, gz.message("data: first\n\n"))
		upstream.data(id, true, gz.message("data: second\n\n")+gz.end())
		if body, ended := inflatedSoFar(client.stream(t, 1).data); body != "data: edited\n\ndata: second\n\n" || !ended {
			t.Fatalf("the client's body, inflated\nwanted:\n%q, the gzip stream ended\ngot:\n%q (ended %v)", "data: edited\n\ndata: second\n\n", body, ended)
		}
		req, _ := streamedResponse(t, captured)
		messages := storedMessages(t, proxyOf(t, addr), req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "data: edited\n\n" || messages[0].Metadata["edited"] != true || messages[0].Compressed {
			t.Fatalf("stored messages\nwanted:\nthe edited event first, not marked compressed\ngot:\n%+v", messages)
		}
	})

	t.Run("should send an injected event to the client inside the stream compressed again, between the origin's events", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)
		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
		id := upstream.nthStream(t, 0)
		gz := newGzipStream()
		upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "gzip")
		upstream.data(id, false, gz.message("data: one\n\n"))
		client.await(t, "the first event", func(p *h2TestPeer) bool {
			s := p.streams[1]
			got := ""
			if s != nil {
				got, _ = inflatedSoFar(s.data)
			}
			return got == "data: one\n\n"
		})
		req := <-captured.request
		injectOnceCarried(t, proxyOf(t, addr), req.ID, "response", "data: injected\n\n", StreamInjectOptions{})
		upstream.data(id, true, gz.message("data: two\n\n")+gz.end())
		if body, ended := inflatedSoFar(client.stream(t, 1).data); body != "data: one\n\ndata: injected\n\ndata: two\n\n" || !ended {
			t.Fatalf("the client's body, inflated\nwanted:\nthe injected event between the two\ngot:\n%q (ended %v)", body, ended)
		}
	})

	readWhole := []struct{ name, encoding, body string }{
		{"a brotli event stream, a coding Marasi does not compress", "br", brotlied(t, "data: one\n\n")},
		{"a gzipped event stream whose head declares a length", "gzip", gzipped(t, "data: one\n\n")},
	}
	for _, c := range readWhole {
		t.Run("should read "+c.name+" whole, as an ordinary pair", func(t *testing.T) {
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			head := []string{":status", "200", "content-type", "text/event-stream", "content-encoding", c.encoding}
			if c.encoding == "gzip" {
				head = append(head, "content-length", strconv.Itoa(len(c.body)))
			}
			upstream.headers(id, false, head...)
			upstream.data(id, true, c.body)
			if body := client.stream(t, 1).data; string(body) != c.body {
				t.Fatalf("the client's body\nwanted:\nthe origin's bytes\ngot:\n%q", body)
			}
			_, res := streamedResponse(t, captured)
			if _, ok := res.Metadata["streamed"]; ok {
				t.Fatalf("the stored response\nwanted:\nno streamed metadata\ngot:\n%v", res.Metadata)
			}
		})
	}

	failures := []struct {
		name, code, errorPrefix string
		limit                   int64
		body                    func(gz *gzipStream) string
		tooLarge                bool
	}{
		{
			name: "a gzipped event stream that turns corrupt", code: "INTERNAL_ERROR",
			body: func(gz *gzipStream) string { return gz.message("data: one\n\n") + "\xff\xff not deflate" },
		},
		{
			name: "a decompression bomb, an event that inflates past the limit", code: "ENHANCE_YOUR_CALM", errorPrefix: "too large", limit: 64, tooLarge: true,
			body: func(gz *gzipStream) string {
				return gz.message("data: one\n\n") + gz.message("data: "+strings.Repeat("a", 4096))
			},
		},
	}
	for _, c := range failures {
		t.Run("should store "+c.name+" as the compressed bytes with its inflate error, reset both legs and log it", func(t *testing.T) {
			logs := &proxyLogs{}
			configure := []func(*Proxy){logs.record}
			if c.limit > 0 {
				configure = append(configure, func(proxy *Proxy) { proxy.Config.StreamMaxMessageSize = c.limit })
			}
			origin := newH2TestOrigin(t)
			addr, captured, ca := serveH1EngineMITMProxy(t, true, configure...)
			client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
			upstream := origin.accept(t)
			client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/events")
			id := upstream.nthStream(t, 0)
			upstream.headers(id, false, ":status", "200", "content-type", "text/event-stream", "content-encoding", "gzip")
			upstream.data(id, false, c.body(newGzipStream()))

			got := client.stream(t, 1)
			upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[id].reset != "" })
			if inflated, _ := inflatedSoFar(got.data); inflated != "data: one\n\n" || got.reset != c.code {
				t.Fatalf("the client's stream\nwanted:\nthe first event, then RST_STREAM %s\ngot:\n%q, %q", c.code, inflated, got.reset)
			}
			req, res := streamedResponse(t, captured)
			messages := storedMessages(t, proxyOf(t, addr), req.ID)
			if len(messages) != 2 || string(messages[0].Payload) != "data: one\n\n" {
				t.Fatalf("stored messages\nwanted:\nthe first event, then the one that failed\ngot:\n%+v", messages)
			}
			failed := messages[1]
			inflateErr, _ := failed.Metadata["inflate_error"].(string)
			if !failed.Compressed || len(failed.Payload) == 0 || inflateErr == "" || !strings.HasPrefix(inflateErr, c.errorPrefix) || failed.Metadata["encoding"] != "gzip" {
				t.Fatalf("the failed message\nwanted:\ncompressed bytes, encoding gzip, an inflate_error starting %q\ngot:\n%+v", c.errorPrefix, failed)
			}
			if c.tooLarge && failed.Metadata["too_large"] != true {
				t.Errorf("the failed message's metadata\nwanted:\ntoo_large\ngot:\n%v", failed.Metadata)
			}
			if d := direction(t, res.Metadata, "response"); d["ending"] != "reset" || d["reset_code"] != c.code {
				t.Errorf("metadata.streamed.response\nwanted:\nreset %s\ngot:\n%v", c.code, d)
			}
			if !logs.find("WARN", "could not be inflated") {
				t.Errorf("proxy logs\nwanted:\na WARN naming the inflate failure\ngot:\n%+v", logs.entries)
			}
		})
	}
}

// readChunk reads one chunk of a chunked body off br within two seconds,
// and reports whether it was the last chunk.
func readChunk(t *testing.T, conn net.Conn, br *bufio.Reader) ([]byte, bool) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading a chunk size line: %v", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
	if err != nil {
		t.Fatalf("a chunk size line: %q", line)
	}
	if n == 0 {
		br.ReadString('\n')
		return nil, true
	}
	data := make([]byte, n+2)
	if _, err := io.ReadFull(br, data); err != nil {
		t.Fatalf("reading a chunk: %v", err)
	}
	return data[:n], false
}

// readInflated reads chunks onto body until its gzip bytes inflate to
// want, before the body ends.
func readInflated(t *testing.T, conn net.Conn, br *bufio.Reader, body []byte, want string) []byte {
	t.Helper()
	for {
		if got, _ := inflatedSoFar(body); got == want {
			return body
		}
		data, last := readChunk(t, conn, br)
		if last {
			got, _ := inflatedSoFar(body)
			t.Fatalf("the client's body, inflated\nwanted, before the body ended:\n%q\ngot:\n%q", want, got)
		}
		body = append(body, data...)
	}
}

// readToLastChunk reads chunks onto body up to the last chunk.
func readToLastChunk(t *testing.T, conn net.Conn, br *bufio.Reader, body []byte) []byte {
	t.Helper()
	for {
		data, last := readChunk(t, conn, br)
		if last {
			return body
		}
		body = append(body, data...)
	}
}

func TestH1Engine_CompressedEventStream(t *testing.T) {
	t.Run("should inflate a gzipped event stream, send each event on to the client as it arrives, compressed again, and store each event decoded", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\nAccept: text/event-stream\r\n\r\n")

		upstream := origin.accept(t)
		gz := newGzipStream()
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk(gz.message("id: 1\ndata: first\n\n")))
		readWithin(t, client, br, "the client", head)
		body := readInflated(t, client, br, nil, "id: 1\ndata: first\n\n")

		io.WriteString(upstream, chunk(gz.message("id: 2\ndata: second\n\n"))+chunk(gz.end())+"0\r\n\r\n")
		body = readToLastChunk(t, client, br, body)
		if got, ended := inflatedSoFar(body); got != "id: 1\ndata: first\n\nid: 2\ndata: second\n\n" || !ended {
			t.Fatalf("the client's body\nwanted:\nthe end of the gzip stream\ngot:\n%q", body)
		}

		req, res := streamedResponse(t, captured)
		if d := direction(t, res.Metadata, "response"); d["framing"] != "sse" || d["messages"] != float64(2) || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.response\nwanted:\nsse, 2 messages, complete\ngot:\n%v", d)
		}
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 2 || string(messages[0].Payload) != "id: 1\ndata: first\n\n" || string(messages[1].Payload) != "id: 2\ndata: second\n\n" || messages[0].Compressed {
			t.Fatalf("stored messages\nwanted:\nthe two events, decoded, not marked compressed\ngot:\n%+v", messages)
		}
	})
}

func TestH1Engine_CompressedStreams(t *testing.T) {
	t.Run("should inflate a deflate NDJSON response read until close and send each line on to the client as it arrives, compressed again", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		proxy, addr, captured := serveFramingProxy(t, nil)
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/logs HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")

		upstream := origin.accept(t)
		var sent bytes.Buffer
		w := zlib.NewWriter(&sent)
		message := func(line string) string {
			w.Write([]byte(line))
			w.Flush()
			out := sent.String()
			sent.Reset()
			return out
		}
		const head = "HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\nContent-Encoding: deflate\r\n\r\n"
		io.WriteString(upstream, head+message("{\"n\":1}\n"))
		readWithin(t, client, br, "the client", head)
		inflated := func(body []byte) string {
			r, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				return ""
			}
			out, _ := io.ReadAll(r)
			return string(out)
		}
		var body []byte
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		for inflated(body) != "{\"n\":1}\n" {
			buf := make([]byte, 512)
			n, err := br.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				t.Fatalf("the client's body, inflated\nwanted, before the body ended:\nthe first line\ngot:\n%q (%v)", inflated(body), err)
			}
		}
		w.Write([]byte("{\"n\":2}\n"))
		w.Close()
		io.WriteString(upstream, sent.String())
		upstream.Close()
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		rest, _ := io.ReadAll(br)
		body = append(body, rest...)
		if got := inflated(body); got != "{\"n\":1}\n{\"n\":2}\n" {
			t.Fatalf("the client's body, inflated\nwanted:\nboth lines\ngot:\n%q", got)
		}

		req, _ := streamedResponse(t, captured)
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 2 || string(messages[1].Payload) != "{\"n\":2}\n" || messages[1].Framing != "ndjson" {
			t.Fatalf("stored messages\nwanted:\nthe two lines, decoded\ngot:\n%+v", messages)
		}
	})

	t.Run("should store a gzipped event stream that turns corrupt with its inflate error and close both connections", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		logs := &proxyLogs{}
		proxy, addr, captured := serveFramingProxy(t, logs.record)
		client, br := dialProxy(t, addr)
		io.WriteString(client, "GET http://"+origin.addr+"/events HTTP/1.1\r\nHost: "+origin.addr+"\r\n\r\n")

		upstream := origin.accept(t)
		const head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nTransfer-Encoding: chunked\r\n\r\n"
		io.WriteString(upstream, head+chunk(newGzipStream().message("data: one\n\n")+"\xff\xff not deflate"))
		readWithin(t, client, br, "the client", head)
		body := readInflated(t, client, br, nil, "data: one\n\n")
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		if rest, err := io.ReadAll(br); err != nil {
			t.Fatalf("the client's connection\nwanted:\nclosed after the first event\ngot:\n%q, %v", append(body, rest...), err)
		}
		upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := upstream.Read(make([]byte, 1)); err == nil {
			t.Fatal("the origin's connection\nwanted:\nclosed\ngot:\nopen")
		}

		req, res := streamedResponse(t, captured)
		messages := storedMessages(t, proxy, req.ID)
		if len(messages) != 2 || !messages[1].Compressed || messages[1].Metadata["inflate_error"] == nil {
			t.Fatalf("stored messages\nwanted:\nthe first event, then the compressed bytes with inflate_error\ngot:\n%+v", messages)
		}
		if d := direction(t, res.Metadata, "response"); d["ending"] != "reset" {
			t.Errorf("metadata.streamed.response\nwanted:\nreset\ngot:\n%v", d)
		}
		if !logs.find("WARN", "could not be inflated") {
			t.Errorf("proxy logs\nwanted:\na WARN naming the inflate failure\ngot:\n%+v", logs.entries)
		}
	})
}
