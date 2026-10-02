package main

import (
	"net/http"
	"testing"
)

func TestFindingListRequests(t *testing.T) {
	testListRequests(t, "finding")
}

func TestTestCaseListRequests(t *testing.T) {
	testListRequests(t, "test-case")
}

func testListRequests(t *testing.T, parent string) {
	t.Helper()
	binary := buildMarasi(t)
	id := "01938032-1b17-7243-b035-e6a9f4645904"
	requestID := "0193802f-f0e7-73d9-a764-06d21e367809"
	for _, asJSON := range []bool{false, true} {
		configDir := serviceConfigDir(t)
		items := `[{"id":"` + requestID + `","method":"GET","host":"example.com","path":"/proof.txt","status_code":200,"length":"5"}]`
		sent := startCannedControlAPI(t, configDir, "work", http.StatusOK, `{"id":"`+id+`","title":"Evidence","items":`+items+`,"artifacts":[]}`)
		args := []string{"--config-dir", configDir, "--instance", "work", parent, "list-requests", id}
		if asJSON {
			args = append(args, "--json")
		}
		stdout, stderr, err := runMarasi(binary, args...)
		want := requestID + "  GET  example.com  /proof.txt  200  5\n"
		if asJSON {
			want = `{"items":` + items + "}\n"
		}
		if err != nil || stdout != want || stderr != "" {
			t.Fatalf("wanted stdout %q, got %q stderr %q error %v", want, stdout, stderr, err)
		}
		if got := sent.snapshot(); got.Method != http.MethodGet || got.Path != "/"+parent+"/"+id {
			t.Fatalf("unexpected request: %s %s", got.Method, got.Path)
		}
	}
}
