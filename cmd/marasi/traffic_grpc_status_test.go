package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestTrafficGRPCStatusOutput(t *testing.T) {
	t.Run("should print a gRPC pair's grpc-status after its status code in the list", func(t *testing.T) {
		configDir := serviceConfigDir(t)
		startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"items":[`+
			`{"id":"01938032-1b17-7243-b035-e6a9f4645904","number":1,"method":"POST","host":"example.com","path":"/a","status_code":200,"length":"0","client_protocol":"h2","origin_protocol":"h2","grpc_status":3},`+
			`{"id":"0193802f-f0e7-73d9-a764-06d21e367809","number":2,"method":"GET","host":"example.com","path":"/b","status_code":200,"length":"2","client_protocol":"h2","origin_protocol":"h2"}`+
			`],"next_cursor":null}`)

		stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "list")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		header := "#  ID                                    METHOD  HOST         PATH  STATUS        LENGTH  PROTOCOL\n"
		want := header +
			"1  01938032-1b17-7243-b035-e6a9f4645904  POST    example.com  /a    200 (grpc 3)  0       h2\n" +
			"2  0193802f-f0e7-73d9-a764-06d21e367809  GET     example.com  /b    200           2       h2\n" +
			header
		if stdout != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
		}
	})

	t.Run("should print a gRPC pair's grpc-status after its status code, and no line for another pair", func(t *testing.T) {
		id := "0193802f-f0e7-73d9-a764-06d21e367809"
		head := `{"id":"` + id + `","number":42,"note":"","metadata":{},"request":{"scheme":"https","method":"POST","host":"example.com","path":"/a","raw":"","requested_at":"2026-01-02T03:04:05Z"},"response":{"status":"200 OK","status_code":200,"content_type":"application/grpc","length":"0","raw":"","responded_at":"2026-01-02T03:04:06Z"},"upstream_proxy":null,"client_protocol":"h2","origin_protocol":"h2"`
		for body, want := range map[string]string{
			head + `,"grpc_status":7}`: "status_code: 200\ngrpc_status: 7\ncontent_type:",
			head + `}`:                 "status_code: 200\ncontent_type:",
		} {
			configDir := serviceConfigDir(t)
			startCannedControlAPI(t, configDir, "work", http.StatusOK, body)
			stdout, _, err := executeRoot(t, "--config-dir", configDir, "--instance", "work", "traffic", "get", id)
			if err != nil {
				t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
			}
			if !strings.Contains(stdout, want) {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("should document the grpc_status query field in help", func(t *testing.T) {
		stdout, _, err := executeRoot(t, "traffic", "list", "--help")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		for _, want := range []string{"  grpc_status\n", "grpc_status = 3"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("\nwanted help to contain:\n%s\ngot:\n%s", want, stdout)
			}
		}
	})
}
