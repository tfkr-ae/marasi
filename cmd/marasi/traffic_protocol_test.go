package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestTrafficProtocolOutput(t *testing.T) {
	t.Run("should print each pair's protocol in the list, and both when they differ", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[`+
			`{"id":"01938032-1b17-7243-b035-e6a9f4645904","number":1,"method":"GET","host":"example.com","path":"/a","status_code":200,"length":"2","client_protocol":"h2","origin_protocol":"h2"},`+
			`{"id":"0193802f-f0e7-73d9-a764-06d21e367809","number":2,"method":"GET","host":"example.com","path":"/b","status_code":200,"length":"2","client_protocol":"http/1.1","origin_protocol":"http/1.1"},`+
			`{"id":"0193802f-f0e7-73d9-a764-06d21e36780a","number":3,"method":"GET","host":"example.com","path":"/c","status_code":200,"length":"2","client_protocol":"h2","origin_protocol":"http/1.1"}`+
			`],"next_cursor":null}`)

		stdout, _, err := runMarasi(buildMarasi(t), "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "#  ID                                    METHOD  HOST         PATH  STATUS  LENGTH  PROTOCOL\n"
		want := header +
			"1  01938032-1b17-7243-b035-e6a9f4645904  GET     example.com  /a    200     2       h2\n" +
			"2  0193802f-f0e7-73d9-a764-06d21e367809  GET     example.com  /b    200     2       http/1.1\n" +
			"3  0193802f-f0e7-73d9-a764-06d21e36780a  GET     example.com  /c    200     2       h2->http/1.1\n" +
			header
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should print the client and origin protocol of a pair after scheme", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		body := `{"id":"` + id + `","number":42,"note":"","metadata":{},"request":{"scheme":"https","method":"GET","host":"example.com","path":"/a","raw":"R0VUIC9hCg==","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/json","length":"12","raw":"aGVsbG8K","responded_at":"2026-01-02T03:04:06Z"},"upstream_proxy":null,"client_protocol":"h2","origin_protocol":"h2"}`
		startCannedControlAPI(t, configDir, "work", http.StatusOK, body)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := "scheme: https\nclient_protocol: h2\norigin_protocol: h2\nmethod: GET\n"
		if !strings.Contains(stdout, want) {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should document the protocol query fields and sorts in help", func(t *testing.T) {
		stdout, _, err := executeRoot(t, "traffic", "list", "--help")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		for _, want := range []string{"client_protocol, origin_protocol", `client_protocol = "h2"`, "client_protocol and\norigin_protocol"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("\nwanted help to contain:\n%s\ngot:\n%s", want, stdout)
			}
		}
	})
}
