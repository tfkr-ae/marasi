package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	armorymanager "github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func TestArmoryTemplateFromStreamedPair(t *testing.T) {
	conn, err := db.New(t.TempDir()+"/armory.marasi", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	repo := db.NewProxyRepo(conn)
	manager, err := armorymanager.NewManager(repo, unusedWordlists{}, func(context.Context, string, uuid.UUID, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(&marasi.Proxy{TrafficRepo: repo, StreamRepo: repo, ArmoryRepo: repo, Armory: manager}, func() {})

	const head = "POST /test.Echo/Collect HTTP/2\r\n:authority: grpc.example\r\n:scheme: https\r\ncontent-type: application/grpc\r\nte: trailers\r\n\r\n"

	// insertStreamedPair stores a pair whose request direction was
	// streamed with framing, its stored request the head with no body.
	insertStreamedPair := func(t *testing.T, head, framing string) uuid.UUID {
		t.Helper()
		id, _ := uuid.NewV7()
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "POST", Host: "grpc.example", Path: "/test.Echo/Collect",
			Raw: []byte(head), Metadata: map[string]any{}, RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		state := domain.StreamedState{Request: &domain.StreamedDirectionState{Framing: framing, Ending: "complete"}}
		if err := repo.RecordStreamedPair(&domain.StreamedPairRecord{PairID: id, State: state}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertMessage := func(t *testing.T, pair uuid.UUID, direction, framing string, payload []byte, metadata map[string]any) {
		t.Helper()
		id, _ := uuid.NewV7()
		flags := uint8(0)
		if metadata == nil {
			metadata = map[string]any{}
		}
		err := repo.InsertStreamMessage(&domain.StreamMessage{
			ID: id, PairID: pair, Direction: direction, Framing: framing, Flags: &flags, Payload: payload, CreatedAt: time.Now(), Metadata: metadata,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	create := func(t *testing.T, id uuid.UUID) armoryTemplate {
		t.Helper()
		response := requestControlAPI(server, http.MethodPost, "/armory/template/from-pair/"+id.String(), "")
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
		var template armoryTemplate
		if err := json.Unmarshal(response.Body.Bytes(), &template); err != nil {
			t.Fatalf("decoding template: %v", err)
		}
		return template
	}

	// 1:LEN "a\b", then 2:VARINT 7, then 1:LEN "x" with 3:VARINT 300.
	first := []byte{0x0a, 0x03, 'a', '\\', 'b'}
	second := []byte{0x10, 0x07}
	third := []byte{0x0a, 0x01, 'x', 0x18, 0xac, 0x02}
	envelope := func(payload []byte) string {
		return string([]byte{0, 0, 0, 0, byte(len(payload))}) + string(payload)
	}

	clientStreaming := insertStreamedPair(t, head, "grpc")
	insertMessage(t, clientStreaming, domain.StreamDirectionRequest, "grpc", first, nil)
	insertMessage(t, clientStreaming, domain.StreamDirectionResponse, "grpc", []byte{0x08, 0x01}, nil)
	insertMessage(t, clientStreaming, domain.StreamDirectionRequest, "grpc", []byte{0x08, 0x63}, map[string]any{"dropped": true})
	insertMessage(t, clientStreaming, domain.StreamDirectionRequest, "grpc", second, nil)
	insertMessage(t, clientStreaming, domain.StreamDirectionRequest, "grpc", third, nil)

	t.Run("should build a grpc template with the captured head and every forwarded request message in order", func(t *testing.T) {
		template := create(t, clientStreaming)
		if template.Framing != "grpc" {
			t.Fatalf("\nwanted:\ngrpc\ngot:\n%q", template.Framing)
		}
		want := head +
			"--- message ---\n1:LEN {\"a\\\\\\\\b\"}\n" +
			"--- message ---\n2:VARINT 7\n" +
			"--- message ---\n1:LEN {\"x\"}\n3:VARINT 300\n"
		if template.RawTemplate != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, template.RawTemplate)
		}
		stored, err := repo.GetArmoryTemplate(template.ID)
		if err != nil || stored == nil || stored.Framing != "grpc" {
			t.Fatalf("\nwanted:\nthe template stored with framing grpc\ngot:\n%+v %v", stored, err)
		}
	})

	t.Run("should render with no payloads to the captured messages' bytes", func(t *testing.T) {
		template := create(t, clientStreaming)
		rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
		if err != nil {
			t.Fatal(err)
		}
		want := head + envelope(first) + envelope(second) + envelope(third)
		if rendered != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, rendered)
		}
	})

	t.Run("should keep a text form's escapes and non-printable bytes through building", func(t *testing.T) {
		// A string with a NUL byte decodes as a byte list, and one with a
		// quote, a backslash and a newline as an escaped string.
		payload := []byte{0x0a, 0x04, 'a', 0x00, '\\', 'c', 0x12, 0x04, '"', '\\', '\n', '@'}
		pair := insertStreamedPair(t, head, "grpc")
		insertMessage(t, pair, domain.StreamDirectionRequest, "grpc", payload, nil)
		template := create(t, pair)
		rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
		if err != nil {
			t.Fatal(err)
		}
		if want := head + envelope(payload); rendered != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q\nfrom template:\n%q", want, rendered, template.RawTemplate)
		}
	})

	t.Run("should be accepted as a run template once a position is added", func(t *testing.T) {
		template := create(t, clientStreaming)
		withPosition := strings.Replace(template.RawTemplate, `{"x"}`, `{"@@x@@"}`, 1)
		body := `{"raw_template":` + jsonString(withPosition) + `,"framing":"grpc","attack_type":"harpoon","wordlists":["w"]}`
		response := requestControlAPI(server, http.MethodPost, "/armory/run/validate", body)
		if response.Code != http.StatusOK {
			t.Fatalf("\nwanted:\n200\ngot:\n%d %s", response.Code, response.Body)
		}
	})

	t.Run("should write a compressed message decompressed under a compressed separator", func(t *testing.T) {
		gzipHead := strings.Replace(head, "te: trailers", "grpc-encoding: gzip\r\nte: trailers", 1)
		pair := insertStreamedPair(t, gzipHead, "grpc")
		id, _ := uuid.NewV7()
		flags := uint8(1)
		err := repo.InsertStreamMessage(&domain.StreamMessage{
			ID: id, PairID: pair, Direction: domain.StreamDirectionRequest, Framing: "grpc", Flags: &flags,
			Payload: first, Compressed: true, CreatedAt: time.Now(), Metadata: map[string]any{"encoding": "gzip"},
		})
		if err != nil {
			t.Fatal(err)
		}
		insertMessage(t, pair, domain.StreamDirectionRequest, "grpc", second, nil)

		template := create(t, pair)

		want := gzipHead +
			"--- message compressed ---\n1:LEN {\"a\\\\\\\\b\"}\n" +
			"--- message ---\n2:VARINT 7\n"
		if template.Framing != "grpc" || template.RawTemplate != want {
			t.Fatalf("\nwanted:\ngrpc %q\ngot:\n%q %q", want, template.Framing, template.RawTemplate)
		}
	})

	t.Run("should build a streamed request whose messages have no text form from the joined request as raw text", func(t *testing.T) {
		thriftHead := strings.Replace(head, "application/grpc", "application/grpc+thrift", 1)
		pair := insertStreamedPair(t, thriftHead, "grpc")
		insertMessage(t, pair, domain.StreamDirectionRequest, "grpc", []byte("a\\b@\xac"), nil)
		template := create(t, pair)
		if template.Framing != "" {
			t.Fatalf("\nwanted:\nno framing\ngot:\n%q", template.Framing)
		}
		// The stored template is what a run sends; JSON carries a byte
		// that is not UTF-8 as U+FFFD.
		stored, err := repo.GetArmoryTemplate(template.ID)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := armorymanager.Render(stored.RawTemplate, stored.Framing)
		if err != nil {
			t.Fatal(err)
		}
		if want := thriftHead + envelope([]byte("a\\b@\xac")); rendered != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, rendered)
		}
	})

	t.Run("should build a gRPC-Web pair with framing grpc-web that renders to the captured bytes", func(t *testing.T) {
		webHead := "POST /test.Echo/Collect HTTP/1.1\r\nHost: grpc.example\r\nContent-Type: application/grpc-web+proto\r\nTransfer-Encoding: chunked\r\n\r\n"
		pair := insertStreamedPair(t, webHead, "grpc-web")
		insertMessage(t, pair, domain.StreamDirectionRequest, "grpc-web", first, nil)
		insertMessage(t, pair, domain.StreamDirectionRequest, "grpc-web", second, nil)
		template := create(t, pair)
		if template.Framing != "grpc-web" {
			t.Fatalf("\nwanted:\ngrpc-web\ngot:\n%q", template.Framing)
		}
		rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
		if err != nil {
			t.Fatal(err)
		}
		want := "POST /test.Echo/Collect HTTP/1.1\r\nHost: grpc.example\r\nContent-Type: application/grpc-web+proto\r\nContent-Length: 17\r\n\r\n" + envelope(first) + envelope(second)
		if rendered != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q\nfrom template:\n%q", want, rendered, template.RawTemplate)
		}
	})

	t.Run("should build a Connect streaming JSON pair with framing connect whose messages render byte for byte", func(t *testing.T) {
		connectHead := "POST /test.Echo/Collect HTTP/2\r\n:authority: grpc.example\r\n:scheme: https\r\ncontent-type: application/connect+json\r\n\r\n"
		pair := insertStreamedPair(t, connectHead, "connect")
		messages := [][]byte{[]byte(`{"a":"C:\\x@@y"}`), []byte("{\"b\":2}\n")}
		for _, message := range messages {
			insertMessage(t, pair, domain.StreamDirectionRequest, "connect", message, nil)
		}
		template := create(t, pair)
		if template.Framing != "connect" {
			t.Fatalf("\nwanted:\nconnect\ngot:\n%q", template.Framing)
		}
		rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
		if err != nil {
			t.Fatal(err)
		}
		if want := connectHead + envelope(messages[0]) + envelope(messages[1]); rendered != want {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q\nfrom template:\n%q", want, rendered, template.RawTemplate)
		}
	})

	// insertPair stores a pair that was not streamed, its request raw.
	insertPair := func(t *testing.T, raw string) uuid.UUID {
		t.Helper()
		id, _ := uuid.NewV7()
		err := repo.InsertRequest(&domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "POST", Host: "grpc.example", Path: "/test.Echo/Say",
			Raw: []byte(raw), Metadata: map[string]any{}, RequestedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	unary := []struct {
		name, contentType, body, want string
	}{
		{name: "proto", contentType: "application/proto", body: string(third), want: "--- message ---\n1:LEN {\"x\"}\n3:VARINT 300\n"},
		{name: "JSON", contentType: "application/json", body: `{"q":"a\\b@@c"}`, want: "--- message ---\n{\"q\":\"a\\\\\\\\b\\@\\@c\"}\n"},
	}
	for _, tt := range unary {
		t.Run("should build a Connect unary "+tt.name+" pair with framing connect and one separator that renders to the captured request", func(t *testing.T) {
			unaryHead := "POST /test.Echo/Say HTTP/1.1\r\nHost: grpc.example\r\nConnect-Protocol-Version: 1\r\nContent-Type: " + tt.contentType + "\r\nContent-Length: " + strconv.Itoa(len(tt.body)) + "\r\n\r\n"
			template := create(t, insertPair(t, unaryHead+tt.body))
			if template.Framing != "connect" {
				t.Fatalf("\nwanted:\nconnect\ngot:\n%q", template.Framing)
			}
			if want := unaryHead + tt.want; template.RawTemplate != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, template.RawTemplate)
			}
			rendered, err := armorymanager.Render(template.RawTemplate, template.Framing)
			if err != nil {
				t.Fatal(err)
			}
			if want := unaryHead + tt.body; rendered != want {
				t.Fatalf("\nwanted:\n%q\ngot:\n%q", want, rendered)
			}
		})
	}
}
