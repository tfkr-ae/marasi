package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

func TestTrafficGetParts(t *testing.T) {
	id := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	row := &domain.RequestResponseRow{
		Request: domain.ProxyRequest{
			ID: id, Scheme: "https", Method: "GET", Host: "example.com", Path: "/a",
			Raw:         []byte("GET /a"),
			RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		Response: domain.ProxyResponse{
			ID: id, Status: "200 OK", StatusCode: 200, ContentType: "text/html", Length: "9",
			Raw:         []byte("<b>&</b>"),
			RespondedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		},
		Metadata:   map[string]any{},
		Prettified: domain.Prettified{Response: "<b>&</b>"},
	}
	head := "{\"id\":\"0193802f-f0e7-73d9-a764-06d21e367809\",\"number\":0,\"note\":\"\",\"metadata\":{},\"highlight\":null,"
	tests := []struct {
		name   string
		query  string
		status int
		want   string
	}{
		{
			name:   "should send both parts by default, with < > & unescaped",
			status: http.StatusOK,
			want: head + "\"request\":{\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"raw\":\"R0VUIC9h\",\"requested_at\":\"2026-01-02T03:04:05Z\"}," +
				"\"response\":{\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"text/html\",\"length\":\"9\",\"raw\":\"PGI+JjwvYj4=\",\"responded_at\":\"2026-01-02T03:04:06Z\"}," +
				"\"prettified\":{\"request\":null,\"response\":\"<b>&</b>\"},\"upstream_proxy\":null}\n",
		},
		{
			name:   "should leave the prettified copies out with prettified=false",
			query:  "?prettified=false",
			status: http.StatusOK,
			want: head + "\"request\":{\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"raw\":\"R0VUIC9h\",\"requested_at\":\"2026-01-02T03:04:05Z\"}," +
				"\"response\":{\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"text/html\",\"length\":\"9\",\"raw\":\"PGI+JjwvYj4=\",\"responded_at\":\"2026-01-02T03:04:06Z\"}," +
				"\"prettified\":{\"request\":null,\"response\":null},\"upstream_proxy\":null}\n",
		},
		{
			name:   "should leave the raw messages out with raw=false",
			query:  "?raw=false&prettified=true",
			status: http.StatusOK,
			want: head + "\"request\":{\"scheme\":\"https\",\"method\":\"GET\",\"host\":\"example.com\",\"path\":\"/a\",\"raw\":null,\"requested_at\":\"2026-01-02T03:04:05Z\"}," +
				"\"response\":{\"status\":\"200 OK\",\"status_code\":200,\"content_type\":\"text/html\",\"length\":\"9\",\"raw\":null,\"responded_at\":\"2026-01-02T03:04:06Z\"}," +
				"\"prettified\":{\"request\":null,\"response\":\"<b>&</b>\"},\"upstream_proxy\":null}\n",
		},
		{
			name:   "should fail with bad_request when raw is not a bool",
			query:  "?raw=nope",
			status: http.StatusBadRequest,
			want:   "{\"error\":\"bad_request\"}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(&marasi.Proxy{TrafficRepo: &stubTrafficRepository{row: row}}, func() {})
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/traffic/"+id.String()+test.query, nil))
			if response.Code != test.status || response.Body.String() != test.want {
				t.Fatalf("\nwanted:\n%d %s\ngot:\n%d %s", test.status, test.want, response.Code, response.Body.String())
			}
		})
	}
}
