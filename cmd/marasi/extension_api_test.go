package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtensionAPICommand(t *testing.T) {
	binary := buildMarasi(t)
	// The reference needs no instance, so an unusable instance name must not stop it.
	run := func(args ...string) (string, string, error) {
		return runMarasi(binary, append([]string{"--config-dir", t.TempDir(), "--instance", "../bad", "extension", "api"}, args...)...)
	}

	t.Run("overview names every part and the colon rule", func(t *testing.T) {
		stdout, stderr, err := run()
		if err != nil || stderr != "" {
			t.Fatalf("\nwanted:\nthe overview\ngot:\nstderr %q, error %v", stderr, err)
		}
		for _, want := range []string{
			"marasi.strings:upper(s)",
			"processRequest(request: req)",
			"interceptRequest(request: req) -> boolean",
			"print(...: any)",
			"marasi.crypto.aes.gcm",
			"RequestBuilder",
			"no string library",
			"marasi extension api NAME",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("\nwanted:\n%q in the overview\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("a library lists its functions with the colon form", func(t *testing.T) {
		stdout, _, err := run("marasi.encoding.hex")
		if err != nil {
			t.Fatalf("\nwanted:\nthe library\ngot:\n%v", err)
		}
		for _, want := range []string{"marasi.encoding.hex:encode(", "marasi.encoding.hex:decode("} {
			if !strings.Contains(stdout, want) {
				t.Errorf("\nwanted:\n%q\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("a library names its sub-libraries", func(t *testing.T) {
		stdout, _, err := run("marasi.crypto.aes")
		if err != nil {
			t.Fatalf("\nwanted:\nthe library\ngot:\n%v", err)
		}
		for _, want := range []string{"marasi.crypto.aes:generate_key(", "marasi.crypto.aes.gcm", "marasi.crypto.aes.cbc"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("\nwanted:\n%q\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("a table with only sub-libraries lists them", func(t *testing.T) {
		stdout, _, err := run("marasi.encoding")
		if err != nil {
			t.Fatalf("\nwanted:\nthe library\ngot:\n%v", err)
		}
		if !strings.Contains(stdout, "marasi.encoding.hex") || !strings.Contains(stdout, "marasi.encoding.json") {
			t.Errorf("\nwanted:\nmarasi.encoding.hex and marasi.encoding.json\ngot:\n%s", stdout)
		}
		stdout, _, err = run("marasi")
		if err != nil || !strings.Contains(stdout, "  marasi.encoding\n") {
			t.Errorf("\nwanted:\nmarasi to list marasi.encoding\ngot:\n%s %v", stdout, err)
		}
	})

	t.Run("one function accepts the dot or colon form", func(t *testing.T) {
		for _, name := range []string{"marasi.repo:set_metadata", "marasi.repo.set_metadata"} {
			stdout, _, err := run(name)
			if err != nil {
				t.Fatalf("\nwanted:\n%s\ngot:\n%v", name, err)
			}
			if !strings.HasPrefix(stdout, "marasi.repo:set_metadata(") {
				t.Errorf("\nwanted:\nthe signature first\ngot:\n%s", stdout)
			}
			if strings.Contains(stdout, "get_metadata") {
				t.Errorf("\nwanted:\nonly set_metadata\ngot:\n%s", stdout)
			}
		}
	})

	t.Run("a type lists its methods", func(t *testing.T) {
		stdout, _, err := run("req")
		if err != nil {
			t.Fatalf("\nwanted:\nthe type\ngot:\n%v", err)
		}
		for _, want := range []string{"req:method() -> string", "req:drop()", "processRequest"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("\nwanted:\n%q\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("one method", func(t *testing.T) {
		stdout, _, err := run("wsmsg:direction")
		if err != nil {
			t.Fatalf("\nwanted:\nthe method\ngot:\n%v", err)
		}
		if !strings.HasPrefix(stdout, "wsmsg:direction() -> string\n") || !strings.Contains(stdout, `"client" or "server"`) {
			t.Errorf("\nwanted:\nwsmsg:direction and its summary\ngot:\n%s", stdout)
		}
	})

	t.Run("hooks", func(t *testing.T) {
		stdout, _, err := run("hooks")
		if err != nil {
			t.Fatalf("\nwanted:\nthe hooks\ngot:\n%v", err)
		}
		for _, want := range []string{"startup()", "processWebSocketMessage(message: wsmsg)", "Called only in checkpoint"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("\nwanted:\n%q\ngot:\n%s", want, stdout)
			}
		}
	})

	t.Run("an unknown name fails", func(t *testing.T) {
		stdout, stderr, err := run("marasi.nope")
		if err == nil || stdout != "" || !strings.Contains(stderr, `no Lua API entry named "marasi.nope"`) {
			t.Errorf("\nwanted:\nan error naming marasi.nope\ngot:\nstdout %q, stderr %q, error %v", stdout, stderr, err)
		}
	})

	t.Run("json overview is the whole reference", func(t *testing.T) {
		stdout, _, err := run("--json")
		if err != nil {
			t.Fatalf("\nwanted:\nJSON\ngot:\n%v", err)
		}
		var ref struct {
			Hooks     []struct{ Name string } `json:"hooks"`
			Libraries []struct{ Name string } `json:"libraries"`
			Types     []struct{ Name string } `json:"types"`
		}
		if err := json.Unmarshal([]byte(stdout), &ref); err != nil {
			t.Fatalf("\nwanted:\none JSON value\ngot:\n%v", err)
		}
		if len(ref.Hooks) != 7 || len(ref.Libraries) == 0 || len(ref.Types) == 0 {
			t.Errorf("\nwanted:\n7 hooks, libraries, and types\ngot:\n%+v", ref)
		}
	})

	t.Run("json for one function", func(t *testing.T) {
		stdout, _, err := run("marasi.strings.upper", "--json")
		if err != nil {
			t.Fatalf("\nwanted:\nJSON\ngot:\n%v", err)
		}
		var entry map[string]string
		if err := json.Unmarshal([]byte(stdout), &entry); err != nil {
			t.Fatalf("\nwanted:\none JSON object\ngot:\n%v", err)
		}
		if entry["kind"] != "function" || entry["name"] != "marasi.strings:upper" || entry["signature"] == "" || entry["summary"] == "" {
			t.Errorf("\nwanted:\nkind function, name marasi.strings:upper, signature, summary\ngot:\n%v", entry)
		}
	})
}
