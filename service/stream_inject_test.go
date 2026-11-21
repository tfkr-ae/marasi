package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// pairOf waits for the fixture's one streamed pair and returns its ID.
func (f *streamCheckpointFixture) pairOf(t *testing.T) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := f.proxy.TrafficRepo.GetRequestResponseSummary()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			return rows[0].ID
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored pairs\nwanted:\n1\ngot:\n%d", len(rows))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamInjectAndCancelRoutes(t *testing.T) {
	t.Run("should inject a message the origin receives, answer with it stored injected, and refuse a bad request or an unknown pair", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		body, sender := io.Pipe()
		defer sender.Close()
		responses := f.call(t, body, -1)
		go func() { _, _ = sender.Write(grpcFrame([]byte("one"))) }()
		if got := f.receive(t); string(got) != "one" {
			t.Fatalf("the origin's first message\nwanted:\n%q\ngot:\n%q", "one", got)
		}
		pair := f.pairOf(t)

		status, answer := f.post(t, "/stream/"+pair.String()+"/inject", fmt.Sprintf(`{"direction":"request","payload":%q,"compress":"never"}`, base64.StdEncoding.EncodeToString([]byte("injected"))))
		if status != http.StatusOK {
			t.Fatalf("injecting\nwanted:\n200\ngot:\n%d %s", status, answer)
		}
		var message struct {
			PairID    uuid.UUID      `json:"pair_id"`
			Direction string         `json:"direction"`
			Payload   []byte         `json:"payload"`
			Metadata  map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(answer), &message); err != nil || message.PairID != pair || message.Direction != "request" ||
			string(message.Payload) != "injected" || message.Metadata["injected"] != true {
			t.Fatalf("the injected message\nwanted:\n%q from the request of the pair, injected\ngot:\n%s", "injected", answer)
		}
		if got := f.receive(t); string(got) != "injected" {
			t.Fatalf("the origin's second message\nwanted:\n%q\ngot:\n%q", "injected", got)
		}

		for _, bad := range []string{`{}`, `{"direction":"sideways","payload":""}`, `{"direction":"request","payload":"%%%"}`, `{"direction":"request","payload":"","compress":"always"}`} {
			if status, answer := f.post(t, "/stream/"+pair.String()+"/inject", bad); status != http.StatusBadRequest {
				t.Errorf("injecting %s\nwanted:\n400\ngot:\n%d %s", bad, status, answer)
			}
		}
		if status, answer := f.post(t, "/stream/"+uuid.NewString()+"/inject", `{"direction":"request","payload":""}`); status != http.StatusNotFound {
			t.Errorf("injecting into an unknown pair\nwanted:\n404\ngot:\n%d %s", status, answer)
		}
		sender.Close()
		response := <-responses
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	})

	t.Run("should refuse an injection into a pair that ended, with a reason", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		response := <-f.call(t, bytes.NewReader(grpcFrame([]byte("only"))), -1)
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		pair := f.pairOf(t)
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, answer := f.post(t, "/stream/"+pair.String()+"/inject", `{"direction":"response","payload":""}`)
			if status == http.StatusConflict {
				var refusal struct {
					Error   string `json:"error"`
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(answer), &refusal) != nil || refusal.Error != "stream_not_open" || refusal.Message == "" {
					t.Fatalf("the refusal\nwanted:\nstream_not_open with a reason\ngot:\n%s", answer)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("injecting into an ended pair\nwanted:\n409\ngot:\n%d %s", status, answer)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("should cancel a running call, and answer conflict for one that is not running", func(t *testing.T) {
		f := newStreamCheckpointFixture(t)
		body, sender := io.Pipe()
		defer sender.Close()
		responses := f.call(t, body, -1)
		go func() { _, _ = sender.Write(grpcFrame([]byte("one"))) }()
		f.receive(t)
		pair := f.pairOf(t)
		response := <-responses

		if status, answer := f.post(t, "/stream/"+pair.String()+"/cancel", ``); status != http.StatusOK {
			t.Fatalf("cancelling\nwanted:\n200\ngot:\n%d %s", status, answer)
		}
		if _, err := io.ReadAll(response.Body); err == nil {
			t.Fatal("the client's response body after the cancel\nwanted:\nan error, the stream was reset\ngot:\nits end")
		}
		response.Body.Close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, answer := f.post(t, "/stream/"+pair.String()+"/cancel", ``)
			if status == http.StatusConflict {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("cancelling again\nwanted:\n409\ngot:\n%d %s", status, answer)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if status, answer := f.post(t, "/stream/"+uuid.NewString()+"/cancel", ``); status != http.StatusNotFound {
			t.Errorf("cancelling an unknown pair\nwanted:\n404\ngot:\n%d %s", status, answer)
		}
	})
}
