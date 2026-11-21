package conformance

import "testing"

func TestDiffFileName(t *testing.T) {
	t.Run("should replace every character upload-artifact refuses in a path", func(t *testing.T) {
		id := `Duplicate Metadata.HTTPVersion:1.TLS:false/a\b"c<d>e|f*g?h` + "\r\n@connect-plain"
		got := diffFileName(id)
		want := "Duplicate Metadata.HTTPVersion_1.TLS_false_a_b_c_d_e_f_g_h__@connect-plain.json"
		if got != want {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", want, got)
		}
	})

	t.Run("should keep an ID with no such character as it is", func(t *testing.T) {
		if got := diffFileName("http2-5.1.2-1@connect-tls"); got != "http2-5.1.2-1@connect-tls.json" {
			t.Fatalf("\nwanted:\nhttp2-5.1.2-1@connect-tls.json\ngot:\n%s", got)
		}
	})
}
