package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSelection_UnmarshalJSON(t *testing.T) {
	first := uuid.MustParse("0193802f-f0e7-73d9-a764-06d21e367809")
	second := uuid.MustParse("01938032-1b17-7243-b035-e6a9f4645904")

	t.Run("should decode an id selection", func(t *testing.T) {
		var got Selection
		body := `{"ids":["0193802f-f0e7-73d9-a764-06d21e367809","01938032-1b17-7243-b035-e6a9f4645904"]}`
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := Selection{IDs: []uuid.UUID{first, second}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should decode a query selection with a normalised exclusion", func(t *testing.T) {
		var got Selection
		body := `{"query":"host = \"example.com\"","exclusion":["Image/*","text/HTML; charset=utf-8"],"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := Selection{Query: &SelectionQuery{Text: `host = "example.com"`, Exclusion: []string{"image/*", "text/html"}, AsOf: second}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want.Query, got.Query)
		}
	})

	t.Run("should decode a query selection with an empty query and no exclusion", func(t *testing.T) {
		var got Selection
		body := `{"query":"","as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := Selection{Query: &SelectionQuery{AsOf: second}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want.Query, got.Query)
		}
	})

	invalid := []struct {
		name string
		body string
	}{
		{"an empty object", `{}`},
		{"null", `null`},
		{"an array", `[]`},
		{"both ids and a query", `{"ids":["0193802f-f0e7-73d9-a764-06d21e367809"],"query":"","as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`},
		{"a query without as_of", `{"query":"host = \"a\""}`},
		{"a query with a null as_of", `{"query":"","as_of":null}`},
		{"a query with a malformed as_of", `{"query":"","as_of":"nope"}`},
		{"ids with as_of", `{"ids":["0193802f-f0e7-73d9-a764-06d21e367809"],"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`},
		{"ids with an exclusion", `{"ids":["0193802f-f0e7-73d9-a764-06d21e367809"],"exclusion":["image/*"]}`},
		{"an exclusion without a query", `{"exclusion":["image/*"],"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`},
		{"an empty id list", `{"ids":[]}`},
		{"null ids", `{"ids":null}`},
		{"a malformed id", `{"ids":["nope"]}`},
		{"an unknown key", `{"ids":["0193802f-f0e7-73d9-a764-06d21e367809"],"extra":1}`},
		{"a query that is not a string", `{"query":5,"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`},
		{"an invalid exclusion", `{"query":"","exclusion":["*/json"],"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`},
	}
	for _, test := range invalid {
		t.Run("should reject "+test.name, func(t *testing.T) {
			var got Selection
			err := json.Unmarshal([]byte(test.body), &got)
			if !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrInvalidSelection, err)
			}
		})
	}

	t.Run("should name the offending exclusion value", func(t *testing.T) {
		var got Selection
		err := json.Unmarshal([]byte(`{"query":"","exclusion":["text/*html"],"as_of":"01938032-1b17-7243-b035-e6a9f4645904"}`), &got)
		if err == nil || !strings.Contains(err.Error(), `"text/*html"`) {
			t.Fatalf("\nwanted:\nan error naming \"text/*html\"\ngot:\n%v", err)
		}
	})

	t.Run("should accept the maximum number of ids", func(t *testing.T) {
		var got Selection
		if err := json.Unmarshal(idSelectionBody(MaxSelectionIDs), &got); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if len(got.IDs) != MaxSelectionIDs {
			t.Fatalf("\nwanted:\n%d\ngot:\n%d", MaxSelectionIDs, len(got.IDs))
		}
	})

	t.Run("should reject more than the maximum number of ids", func(t *testing.T) {
		var got Selection
		err := json.Unmarshal(idSelectionBody(MaxSelectionIDs+1), &got)
		if !errors.Is(err, ErrInvalidSelection) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrInvalidSelection, err)
		}
	})
}

func idSelectionBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"ids":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"00000000-0000-7000-8000-%012x"`, i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func TestNormalizeExclusion(t *testing.T) {
	t.Run("should lowercase values and drop their parameters", func(t *testing.T) {
		got, err := NormalizeExclusion([]string{"Text/HTML; Charset=UTF-8", "IMAGE/*", "application/vnd.api+json"})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		want := []string{"text/html", "image/*", "application/vnd.api+json"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	for _, value := range []string{"*/json", "ima*", "text/*html", "*", "*/*", "", "text", "text/html/extra"} {
		t.Run("should reject "+value+" and name it", func(t *testing.T) {
			_, err := NormalizeExclusion([]string{"image/png", value})
			var queryErr *QueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("\nwanted:\n*QueryError\ngot:\n%v", err)
			}
			if !strings.Contains(queryErr.Message, fmt.Sprintf("%q", value)) {
				t.Fatalf("\nwanted:\na message naming %q\ngot:\n%s", value, queryErr.Message)
			}
		})
	}
}
