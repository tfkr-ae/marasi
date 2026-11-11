package db

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode/utf8"

	"modernc.org/sqlite"
)

// pairTextCheckFunction is the SQL function that checks a body index text
// term against the text a pair's body index row was built from. A text term
// is one FTS5 phrase of the term's trigrams, and FTS5 lists every position
// of every trigram in every matching row before it answers. A term whose
// trigrams repeat through the text, such as a run of one character in padded
// bodies, then costs far more than reading the text: 39 seconds over 90,000
// pairs. The query finds candidates by the trigrams alone, which needs no
// positions, and this function settles each candidate by searching the text.
//
// pair_text_check(request_raw, response_raw, column, term) returns 1 when
// the text has the term, 0 when it has not, and NULL when it cannot tell
// without FTS5's own case folding. The caller then asks FTS5 for that pair.
// column is a body index column, or "" for all four.
const pairTextCheckFunction = "pair_text_check"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(pairTextCheckFunction, 4, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		requestRaw, _ := args[0].([]byte)
		responseRaw, _ := args[1].([]byte)
		column, ok := args[2].(string)
		if !ok {
			return nil, fmt.Errorf("%s: column is %T, not text", pairTextCheckFunction, args[2])
		}
		term, ok := args[3].(string)
		if !ok {
			return nil, fmt.Errorf("%s: term is %T, not text", pairTextCheckFunction, args[3])
		}
		match, known, err := pairTextCheck(requestRaw, responseRaw, column, term)
		switch {
		case err != nil:
			return nil, err
		case !known:
			return nil, nil
		case match:
			return int64(1), nil
		default:
			return int64(0), nil
		}
	})
}

// pairTextCheck reports whether the body index text of column, built from
// the raw request and response as the index builds it, contains term as the
// index's trigram tokenizer matches it, ignoring case. known is false when
// only FTS5 can answer.
//
// The search folds ASCII letters only. Equal folded text means every
// character is the same or differs only in ASCII case, which FTS5 folds the
// same way, so a find is a match. FTS5 also folds other letters, by tables
// that differ from Go's, and some of them fold to ASCII (the Kelvin sign to
// k), so a miss is final only when the term and every searched text are
// ASCII.
func pairTextCheck(requestRaw, responseRaw []byte, column, term string) (match, known bool, err error) {
	if !utf8.ValidString(term) {
		return false, false, nil
	}
	row := newTrafficIndexRow(storedPair{RequestRaw: requestRaw, ResponseRaw: responseRaw})
	var texts []any
	switch column {
	case "":
		texts = []any{row.requestHead, row.requestBody, row.responseHead, row.responseBody}
	case "request_head":
		texts = []any{row.requestHead}
	case "request_body":
		texts = []any{row.requestBody}
	case "response_head":
		texts = []any{row.responseHead}
	case "response_body":
		texts = []any{row.responseBody}
	default:
		return false, false, fmt.Errorf("%s: unknown column %q", pairTextCheckFunction, column)
	}
	needle, final := foldASCII(term)
	for _, value := range texts {
		text, _ := value.(string)
		folded, ascii := foldASCII(text)
		if strings.Contains(folded, needle) {
			return true, true, nil
		}
		final = final && ascii
	}
	return false, final, nil
}

// foldASCII returns s with ASCII letters in lower case, and whether s is all
// ASCII. It copies s only when s has an upper-case ASCII letter.
func foldASCII(s string) (string, bool) {
	ascii, upper := true, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf {
			ascii = false
		} else if 'A' <= c && c <= 'Z' {
			upper = true
		}
	}
	if !upper {
		return s, ascii
	}
	folded := []byte(s)
	for i, c := range folded {
		if 'A' <= c && c <= 'Z' {
			folded[i] = c + ('a' - 'A')
		}
	}
	return string(folded), ascii
}
