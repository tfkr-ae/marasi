package marasi

import (
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// holdEverything holds every request and response in Checkpoint.
func holdEverything(t *testing.T) func(proxy *Proxy) {
	return func(proxy *Proxy) {
		ext := *testExtensions["checkpoint"]
		ext.Enabled = true
		if err := proxy.WithOptions(WithExtension(&ext)); err != nil {
			t.Fatalf("adding Checkpoint: %v", err)
		}
		proxy.SetIntercept(true)
		proxy.AddRequestPairModifier(CheckpointRequestModifier)
		proxy.AddResponsePairModifier(CheckpointResponseModifier)
	}
}

func TestH2Engine_Checkpoint(t *testing.T) {
	t.Run("should show a held request in the text form and send an edit as typed to the held request's origin", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, false, ":method", "POST", ":scheme", "https", ":authority", origin.addr, ":path", "/a", "content-length", "3")
		client.data(1, true, "abc")

		shown := "POST /a HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\ncontent-length: 3\r\n\r\nabc"
		edit := "POST /b HTTP/2\r\n:scheme: https\r\n:authority: evil.invalid\r\nX-Upper: One\r\ntransfer-encoding: chunked\r\nconnection: close\r\ncontent-length: 99\r\n\r\nedited"
		forwardHeld(t, proxy, shown, edit)

		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		want := h2Fields{{":method", "POST", false}, {":scheme", "https", false}, {":authority", "evil.invalid", false}, {":path", "/b", false}, {"X-Upper", "One", false}, {"transfer-encoding", "chunked", false}, {"connection", "close", false}, {"content-length", "99", false}}
		if len(request.headers) != 1 || !equalFields(request.headers[0], want) || string(request.data) != "edited" {
			t.Fatalf("the origin's request\nwanted:\n%v %q\ngot:\n%v %q", want, "edited", request.headers, request.data)
		}
	})

	t.Run("should send a request and response forwarded as shown exactly as they came, even ones that break RFC 9113", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		sent := h2Fields{{":scheme", "https", false}, {":method", "GET", false}, {"X-Upper", "  padded", false}, {":authority", origin.addr, false}, {"dup", "1", false}, {"dup", "", false}, {":path", "", false}}
		var pairs []string
		for _, f := range sent {
			pairs = append(pairs, f.Name, f.Value)
		}
		client.headers(1, true, pairs...)
		shown := "GET  HTTP/2\r\n:scheme: https\r\nX-Upper:   padded\r\n:authority: " + origin.addr + "\r\ndup: 1\r\ndup: \r\n\r\n"
		forwardHeld(t, proxy, shown, shown)

		id := upstream.nthStream(t, 0)
		if request := upstream.stream(t, id); len(request.headers) != 1 || !equalFields(request.headers[0], sent) {
			t.Fatalf("the origin's request\nwanted:\n%v\ngot:\n%v", sent, request.headers)
		}
		upstream.headers(id, false, ":status", "200", "Content-Length", "1", "connection", "keep-alive")
		upstream.data(id, true, "body")
		shownResponse := "HTTP/2 200\r\nContent-Length: 1\r\nconnection: keep-alive\r\n\r\nbody"
		forwardHeld(t, proxy, shownResponse, shownResponse)

		response := client.stream(t, 1)
		want := h2Fields{{":status", "200", false}, {"Content-Length", "1", false}, {"connection", "keep-alive", false}}
		if len(response.headers) != 1 || !equalFields(response.headers[0], want) || string(response.data) != "body" {
			t.Fatalf("the client's response\nwanted:\n%v %q\ngot:\n%v %q", want, "body", response.headers, response.data)
		}
	})

	t.Run("should send an edited response as typed", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, captured, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/")
		shown := "GET / HTTP/2\r\n:scheme: https\r\n:authority: " + origin.addr + "\r\n\r\n"
		forwardHeld(t, proxy, shown, shown)
		id := upstream.nthStream(t, 0)
		upstream.headers(id, false, ":status", "200", "content-length", "2")
		upstream.data(id, true, "ok")
		edit := "HTTP/2 403 Forbidden\nX-Edited: Yes\nTransfer-Encoding: chunked\ncontent-length: 2\n\nchanged"
		forwardHeld(t, proxy, "HTTP/2 200\r\ncontent-length: 2\r\n\r\nok", edit)

		response := client.stream(t, 1)
		want := h2Fields{{":status", "403", false}, {"X-Edited", "Yes", false}, {"Transfer-Encoding", "chunked", false}, {"content-length", "2", false}}
		if len(response.headers) != 1 || !equalFields(response.headers[0], want) || string(response.data) != "changed" {
			t.Fatalf("the client's response\nwanted:\n%v %q\ngot:\n%v %q", want, "changed", response.headers, response.data)
		}
		<-captured.request
		expectCapturedResponse(t, captured, "HTTP/2 403\r\nX-Edited: Yes\r\nTransfer-Encoding: chunked\r\ncontent-length: 2\r\n\r\nchanged")
	})

	t.Run("should hold only its own stream", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, holdEverything(t))
		proxy := proxyOf(t, addr)
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/held")
		held := waitForCheckpoint(t, proxy, 1)[0]
		client.headers(3, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/free")
		var free domain.CheckpointItem
		for _, item := range waitForCheckpoint(t, proxy, 2) {
			if item.ID != held.ID {
				free = item
			}
		}
		if err := proxy.ForwardCheckpoint(free.ID, CheckpointForward{}); err != nil {
			t.Fatalf("forwarding: %v", err)
		}
		id := upstream.nthStream(t, 0)
		if path, _ := upstream.stream(t, id).headers[0].get(":path"); path != "/free" {
			t.Fatalf("the origin's first request\nwanted:\n/free\ngot:\n%q", path)
		}
		upstream.headers(id, true, ":status", "204")
		response := waitForCheckpoint(t, proxy, 2)
		for _, item := range response {
			if item.ID != held.ID {
				proxy.ForwardCheckpoint(item.ID, CheckpointForward{})
			}
		}
		if status, _ := client.stream(t, 3).headers[0].get(":status"); status != "204" {
			t.Fatalf("the free stream, while the other is held\nwanted:\n204\ngot:\n%v", status)
		}
	})

}

func TestH2Engine_Extensions(t *testing.T) {
	t.Run("should fold a Lua extension's header edits back into the field list by the HTTP/1.1 rules and give Lua the protocol h2", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		addr, _, ca := serveH1EngineMITMProxy(t, true, func(proxy *Proxy) {
			ext := &domain.Extension{Name: "workshop", ID: testExtensions["workshop"].ID, Enabled: true, LuaContent: `
				function processRequest(request)
					request:headers():set("X-Changed", "new")
					request:headers():set("X-Added", request:protocol())
				end
				function processResponse(response)
					response:headers():set("X-Protocol", response:protocol())
				end
			`}
			if err := proxy.WithOptions(WithExtension(ext)); err != nil {
				t.Fatalf("adding the extension: %v", err)
			}
			proxy.AddRequestPairModifier(ExtensionsRequestModifier)
			proxy.AddResponsePairModifier(ExtensionsResponseModifier)
		})
		client, _ := dialH2(t, addr, origin.addr, "127.0.0.1", ca, "h2")
		upstream := origin.accept(t)

		client.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", origin.addr, ":path", "/", "x-changed", "old", "Kept-Case", "1", "x-changed", "second")
		id := upstream.nthStream(t, 0)
		want := h2Fields{{":method", "GET", false}, {":scheme", "https", false}, {":authority", origin.addr, false}, {":path", "/", false}, {"x-changed", "new", false}, {"Kept-Case", "1", false}, {"x-added", "h2", false}}
		if request := upstream.stream(t, id); len(request.headers) != 1 || !equalFields(request.headers[0], want) {
			t.Fatalf("the origin's request\nwanted:\n%v\ngot:\n%v", want, request.headers)
		}
		upstream.headers(id, true, ":status", "204")
		wantResponse := h2Fields{{":status", "204", false}, {"x-protocol", "h2", false}}
		if response := client.stream(t, 1); !equalFields(response.headers[0], wantResponse) {
			t.Fatalf("the client's response\nwanted:\n%v\ngot:\n%v", wantResponse, response.headers)
		}
	})
}
