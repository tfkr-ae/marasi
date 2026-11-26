package marasi

import (
	"context"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/http2"

	"github.com/tfkr-ae/marasi/domain"
)

func TestProxy_LaunchStreamedPair(t *testing.T) {
	t.Run("should replay a client-streaming gRPC launch written as HTTP/2 and store the response as stream messages", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		body := grpcEnvelope(0, "one") + grpcEnvelope(0, "two") + grpcEnvelope(0, "three")
		raw := "POST /test.Echo/Collect HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n" + body

		done := launchH2(t, h, raw)
		upstream := origin.accept(t)
		id := upstream.nthStream(t, 0)
		request := upstream.stream(t, id)
		if string(request.data) != body {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q", body, request.data)
		}
		upstream.headers(id, false, ":status", "200", "content-type", "application/grpc")
		upstream.data(id, false, grpcEnvelope(0, "collected 3"))
		upstream.headers(id, true, "grpc-status", "0")

		launch := waitLaunch(t, done)
		if launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
		messages := storedMessages(t, h.proxy, launch.ID)
		want := []struct{ direction, payload string }{{"request", "one"}, {"request", "two"}, {"request", "three"}, {"response", "collected 3"}}
		if len(messages) != len(want) {
			t.Fatalf("stored messages\nwanted:\n%d\ngot:\n%d %+v", len(want), len(messages), messages)
		}
		for i, w := range want {
			if messages[i].Direction != w.direction || string(messages[i].Payload) != w.payload {
				t.Errorf("message %d\nwanted:\n%s %q\ngot:\n%s %q", i, w.direction, w.payload, messages[i].Direction, messages[i].Payload)
			}
		}
		stored, err := h.proxy.GetLaunch(launch.ID)
		if err != nil || stored.Status != domain.LaunchCompleted {
			t.Fatalf("the stored launch\nwanted:\ncompleted\ngot:\n%+v %v", stored, err)
		}
		metadata, err := h.proxy.TrafficRepo.GetMetadata(launch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d := direction(t, metadata, "response"); d["ending"] != "complete" || d["messages"] != float64(1) {
			t.Errorf("metadata.streamed.response\nwanted:\ncomplete, 1 message\ngot:\n%v", d)
		}
		if outcome, _ := metadata["launch"].(map[string]any); outcome["status"] != "completed" {
			t.Errorf("metadata.launch\nwanted:\ncompleted\ngot:\n%v", metadata["launch"])
		}
	})

	t.Run("should replay a gRPC-Web launch over HTTP/1.1 and store the response as stream messages", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{})
		launchpad, err := h.repository.CreateLaunchpad("grpc-web", "")
		if err != nil {
			t.Fatal(err)
		}
		body := grpcEnvelope(0, "one") + grpcEnvelope(0, "two")
		raw := "POST /test.Echo/Collect HTTP/1.1\r\nHost: " + origin.addr + "\r\nContent-Type: application/grpc-web+proto\r\nContent-Length: 0\r\n\r\n" + body

		done := make(chan domain.Launch, 1)
		go func() {
			launch, err := h.proxy.LaunchSync(context.Background(), raw, launchpad.String(), false)
			if err != nil {
				t.Errorf("LaunchSync() returned unexpected error: %v", err)
			}
			done <- launch
		}()
		upstream := origin.accept(t)
		if got := upstream.head.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
			t.Fatalf("the origin's Content-Length\nwanted:\n%d\ngot:\n%q", len(body), got)
		}
		got := make([]byte, len(body))
		if _, err := io.ReadFull(upstream.br, got); err != nil || string(got) != body {
			t.Fatalf("the origin's request body\nwanted:\n%q\ngot:\n%q (%v)", body, got, err)
		}
		io.WriteString(upstream, "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"+
			chunk(grpcEnvelope(0, "collected 2"))+chunk(grpcEnvelope(0x80, "grpc-status: 0\r\n"))+"0\r\n\r\n")

		launch := waitLaunch(t, done)
		if launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
		messages := storedMessages(t, h.proxy, launch.ID)
		want := []struct{ direction, payload string }{{"request", "one"}, {"request", "two"}, {"response", "collected 2"}, {"response", "grpc-status: 0\r\n"}}
		if len(messages) != len(want) {
			t.Fatalf("stored messages\nwanted:\n%d\ngot:\n%d %+v", len(want), len(messages), messages)
		}
		for i, w := range want {
			if messages[i].Direction != w.direction || string(messages[i].Payload) != w.payload || messages[i].Framing != "grpc-web" {
				t.Errorf("message %d\nwanted:\n%s %q grpc-web\ngot:\n%s %q %s", i, w.direction, w.payload, messages[i].Direction, messages[i].Payload, messages[i].Framing)
			}
		}
	})

	t.Run("should reset the origin stream with CANCEL when a streamed HTTP/2 launch is cancelled, and end the launch cancelled", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		launchpad, err := h.repository.CreateLaunchpad("h2", "")
		if err != nil {
			t.Fatal(err)
		}
		raw := "POST /test.Echo/Watch HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\n\r\n" + grpcEnvelope(0, "watch")

		id, err := h.proxy.Launch(raw, launchpad.String(), true)
		if err != nil {
			t.Fatal(err)
		}
		upstream := origin.accept(t)
		sid := upstream.nthStream(t, 0)
		upstream.stream(t, sid)
		upstream.headers(sid, false, ":status", "200", "content-type", "application/grpc")
		upstream.data(sid, false, grpcEnvelope(0, "tick"))
		h.proxy.awaitStreamMessages(t, id, 2)

		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}
		upstream.await(t, "the reset", func(p *h2TestPeer) bool { return p.streams[sid].reset != "" })
		if reset := arrived(upstream, sid).reset; reset != "CANCEL" {
			t.Fatalf("the origin stream\nwanted:\nRST_STREAM CANCEL\ngot:\n%q", reset)
		}
		launch := awaitLaunchEnded(t, h.proxy, id)
		if launch.Status != domain.LaunchCancelled {
			t.Fatalf("the launch\nwanted:\ncancelled\ngot:\n%+v", launch)
		}
		metadata := awaitStoredMetadata(t, h.proxy, id, "streamed", "launch")
		if outcome, _ := metadata["launch"].(map[string]any); outcome["status"] != "cancelled" {
			t.Errorf("metadata.launch\nwanted:\ncancelled\ngot:\n%v", metadata["launch"])
		}
		if d := direction(t, metadata, "response"); d["ending"] != "cancelled" {
			t.Errorf("metadata.streamed.response\nwanted:\ncancelled\ngot:\n%v", d)
		}
	})

	t.Run("should close the origin connection when a streamed HTTP/1.1 launch is cancelled, and end the launch cancelled", func(t *testing.T) {
		origin := newH1ScriptedOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{})
		launchpad, err := h.repository.CreateLaunchpad("grpc-web", "")
		if err != nil {
			t.Fatal(err)
		}
		body := grpcEnvelope(0, "watch")
		raw := "POST /test.Echo/Watch HTTP/1.1\r\nHost: " + origin.addr + "\r\nContent-Type: application/grpc-web+proto\r\n\r\n" + body

		id, err := h.proxy.Launch(raw, launchpad.String(), false)
		if err != nil {
			t.Fatal(err)
		}
		upstream := origin.accept(t)
		io.ReadFull(upstream.br, make([]byte, len(body)))
		io.WriteString(upstream, "HTTP/1.1 200 OK\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"+chunk(grpcEnvelope(0, "tick")))
		h.proxy.awaitStreamMessages(t, id, 2)

		if _, err := h.proxy.CancelLaunch(id); err != nil {
			t.Fatalf("CancelLaunch() returned unexpected error: %v", err)
		}
		if n, err := upstream.br.Read(make([]byte, 1)); err == nil {
			t.Fatalf("the origin connection\nwanted:\nclosed\ngot:\n%d more bytes", n)
		}
		launch := awaitLaunchEnded(t, h.proxy, id)
		if launch.Status != domain.LaunchCancelled {
			t.Fatalf("the launch\nwanted:\ncancelled\ngot:\n%+v", launch)
		}
		metadata := awaitStoredMetadata(t, h.proxy, id, "streamed", "launch")
		if outcome, _ := metadata["launch"].(map[string]any); outcome["status"] != "cancelled" {
			t.Errorf("metadata.launch\nwanted:\ncancelled\ngot:\n%v", metadata["launch"])
		}
		if d := direction(t, metadata, "response"); d["ending"] != "cancelled" {
			t.Errorf("metadata.streamed.response\nwanted:\ncancelled\ngot:\n%v", d)
		}
	})

	t.Run("should fail a streamed HTTP/2 launch whose origin resets the stream", func(t *testing.T) {
		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		raw := "POST /test.Echo/Collect HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\n\r\n" + grpcEnvelope(0, "one")

		done := launchH2(t, h, raw)
		upstream := origin.accept(t)
		sid := upstream.nthStream(t, 0)
		upstream.stream(t, sid)
		upstream.headers(sid, false, ":status", "200", "content-type", "application/grpc")
		upstream.write(func(f *http2.Framer) error { return f.WriteRSTStream(sid, http2.ErrCodeInternal) })

		if launch := waitLaunch(t, done); launch.Status != domain.LaunchFailed {
			t.Fatalf("the launch\nwanted:\nfailed\ngot:\n%+v", launch)
		}
	})

	// A streamed pair queues its deferred response capture in streamedPair.finish,
	// after the final frame has already gone to the client. SendArmoryRequest and
	// LaunchSync must not return before the server-side handler has finished and
	// queued that response, or callers reading metadata.streamed right after see
	// only the request's metadata.
	t.Run("should wait for the streamed pair to finish before SendArmoryRequest returns", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		beforeStreamedPairFinish = func() {
			close(entered)
			<-release
		}
		t.Cleanup(func() { beforeStreamedPairFinish = nil })

		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		runID := uuid.Must(uuid.NewV7())
		raw := "POST /test.Echo/Say HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n" + grpcEnvelope(0, "hello")

		sent := make(chan error, 1)
		go func() { sent <- h.proxy.SendArmoryRequest(context.Background(), raw, runID, true) }()

		upstream := origin.accept(t)
		sid := upstream.nthStream(t, 0)
		upstream.stream(t, sid)
		upstream.headers(sid, false, ":status", "200", "content-type", "application/grpc")
		upstream.data(sid, false, grpcEnvelope(0, "world"))
		upstream.headers(sid, true, "grpc-status", "0")

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for streamedPair.finish")
		}
		select {
		case err := <-sent:
			t.Fatalf("SendArmoryRequest returned before streamedPair.finish queued the response (err=%v)", err)
		case <-time.After(50 * time.Millisecond):
		}

		unblock()
		if err := <-sent; err != nil {
			t.Fatalf("SendArmoryRequest() returned unexpected error: %v", err)
		}
		stored := capturedRequest(t, h)
		if err := h.proxy.flushDBWrites(); err != nil {
			t.Fatal(err)
		}
		metadata, err := h.proxy.TrafficRepo.GetMetadata(stored.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d := direction(t, metadata, "request"); d["framing"] != "grpc" || d["ending"] != "complete" {
			t.Fatalf("metadata.streamed.request\nwanted:\ngrpc, complete\ngot:\n%v", d)
		}
	})

	t.Run("should wait for the streamed pair to finish before LaunchSync returns over HTTP/2", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		beforeStreamedPairFinish = func() {
			close(entered)
			<-release
		}
		t.Cleanup(func() { beforeStreamedPairFinish = nil })

		origin := newH2TestOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{secureOrigin: true})
		raw := "POST /test.Echo/Collect HTTP/2\r\n:authority: " + origin.addr + "\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n" + grpcEnvelope(0, "one")

		done := launchH2(t, h, raw)
		upstream := origin.accept(t)
		sid := upstream.nthStream(t, 0)
		upstream.stream(t, sid)
		upstream.headers(sid, false, ":status", "200", "content-type", "application/grpc")
		upstream.data(sid, false, grpcEnvelope(0, "reply"))
		upstream.headers(sid, true, "grpc-status", "0")

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for streamedPair.finish")
		}
		select {
		case launch := <-done:
			t.Fatalf("LaunchSync returned before streamedPair.finish queued the response (%+v)", launch)
		case <-time.After(50 * time.Millisecond):
		}

		unblock()
		launch := waitLaunch(t, done)
		if launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
		metadata, err := h.proxy.TrafficRepo.GetMetadata(launch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d := direction(t, metadata, "response"); d["framing"] != "grpc" || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.response\nwanted:\ngrpc, complete\ngot:\n%v", d)
		}
		if outcome, _ := metadata["launch"].(map[string]any); outcome["status"] != "completed" {
			t.Errorf("metadata.launch\nwanted:\ncompleted\ngot:\n%v", metadata["launch"])
		}
	})

	t.Run("should wait for the streamed pair to finish before LaunchSync returns over HTTP/1.1 when the connection closes", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		beforeStreamedPairFinish = func() {
			close(entered)
			<-release
		}
		t.Cleanup(func() { beforeStreamedPairFinish = nil })

		origin := newH1ScriptedOrigin(t)
		h := startViewHarness(t, viewHarnessConfig{})
		launchpad, err := h.repository.CreateLaunchpad("grpc-web-close", "")
		if err != nil {
			t.Fatal(err)
		}
		body := grpcEnvelope(0, "one")
		raw := "POST /test.Echo/Collect HTTP/1.1\r\nHost: " + origin.addr + "\r\nConnection: close\r\nContent-Type: application/grpc-web+proto\r\n\r\n" + body

		done := make(chan domain.Launch, 1)
		go func() {
			launch, err := h.proxy.LaunchSync(context.Background(), raw, launchpad.String(), false)
			if err != nil {
				t.Errorf("LaunchSync() returned unexpected error: %v", err)
			}
			done <- launch
		}()
		upstream := origin.accept(t)
		io.ReadFull(upstream.br, make([]byte, len(body)))
		io.WriteString(upstream, "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"+
			chunk(grpcEnvelope(0, "reply"))+chunk(grpcEnvelope(0x80, "grpc-status: 0\r\n"))+"0\r\n\r\n")

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for streamedPair.finish")
		}
		select {
		case launch := <-done:
			t.Fatalf("LaunchSync returned before streamedPair.finish queued the response (%+v)", launch)
		case <-time.After(50 * time.Millisecond):
		}

		unblock()
		launch := waitLaunch(t, done)
		if launch.Status != domain.LaunchCompleted {
			t.Fatalf("the launch\nwanted:\ncompleted\ngot:\n%+v", launch)
		}
		metadata, err := h.proxy.TrafficRepo.GetMetadata(launch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d := direction(t, metadata, "response"); d["framing"] != "grpc-web" || d["ending"] != "complete" {
			t.Errorf("metadata.streamed.response\nwanted:\ngrpc-web, complete\ngot:\n%v", d)
		}
		if outcome, _ := metadata["launch"].(map[string]any); outcome["status"] != "completed" {
			t.Errorf("metadata.launch\nwanted:\ncompleted\ngot:\n%v", metadata["launch"])
		}
	})
}

// awaitStreamMessages waits until n stream messages of pair are stored.
func (proxy *Proxy) awaitStreamMessages(t *testing.T, pair uuid.UUID, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := proxy.flushDBWrites(); err != nil {
			t.Fatal(err)
		}
		messages, _, err := proxy.StreamRepo.ListStreamMessages(pair, nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored messages\nwanted:\n%d\ngot:\n%d", n, len(messages))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitLaunchEnded waits until the launch id reaches a terminal state.
func awaitLaunchEnded(t *testing.T, proxy *Proxy, id uuid.UUID) domain.Launch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		launch, err := proxy.GetLaunch(id)
		if err == nil && launch.Status != domain.LaunchPending {
			return launch
		}
		if time.Now().After(deadline) {
			t.Fatalf("the launch\nwanted:\nended\ngot:\n%+v %v", launch, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitStoredMetadata waits until the stored metadata of pair has every key.
func awaitStoredMetadata(t *testing.T, proxy *Proxy, pair uuid.UUID, keys ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := proxy.flushDBWrites(); err != nil {
			t.Fatal(err)
		}
		metadata, err := proxy.TrafficRepo.GetMetadata(pair)
		missing := err != nil
		for _, key := range keys {
			if _, ok := metadata[key]; !ok {
				missing = true
			}
		}
		if !missing {
			return metadata
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stored metadata\nwanted:\n%v\ngot:\n%v %v", keys, metadata, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
