package fixture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// words is the vocabulary that text bodies, paths, and notes are built from.
var words = strings.Fields(`account action active address admin agent alert amount api app archive asset
audit auth avatar balance banner batch billing block board body brand browser bucket budget build bundle
button cache campaign card cart catalog category channel chart check client cloud cluster code comment
company config connect contact content context contract cookie count country coupon course credit
customer dashboard data date debug default delete delivery deploy detail device digest discount document
domain draft driver edit editor email endpoint engine entry error event export feature feed field file
filter flag folder footer form frame gateway group guest header health history home host hook image
import index info input invoice issue item job key label language layout lead level license limit link
list locale log login logout manager map market media member menu message meta metric mobile mode model
module monitor network node notice notify number offer office order owner package page panel partner
password path payment people permission phone photo picture plan player policy portal post preview price
print privacy product profile project promo proxy public query queue quota rate reader record redirect
region report request resource result review role route rule sale sample scan schedule schema scope
score search secret section security segment seller server service session setting share shipping shop
signal signup site size slot source space stage state static status stock storage store stream style
subject submit summary support survey sync system table tag target task team template tenant test theme
thread ticket time token topic total track traffic transfer trigger type update upload usage user
value vendor version video view visit wallet warning watch widget window work worker zone`)

// word returns a random vocabulary word.
func word(rng *rand.Rand) string {
	return words[rng.IntN(len(words))]
}

// sentence returns a few words separated by spaces.
func sentence(rng *rand.Rand, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(word(rng))
	}
	return b.String()
}

// logNormal draws from a log-normal distribution with the given median and
// sigma, clamped to [low, high].
func logNormal(rng *rand.Rand, median, sigma float64, low, high int) int {
	v := int(median * math.Exp(sigma*rng.NormFloat64()))
	return max(low, min(high, v))
}

// textBody returns a body of about size bytes in the shape of kind.
func textBody(rng *rand.Rand, kind bodyKind, size int, plant string) []byte {
	var b bytes.Buffer
	b.Grow(size + 256)
	switch kind {
	case bodyJSON:
		b.WriteString(`{"data":[`)
		if plant != "" {
			fmt.Fprintf(&b, `{"comment":%q},`, plant)
		}
		for i := 0; b.Len() < size || i == 0; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"name":%q,"email":"%s.%s@example.com","active":%t,"tags":[%q,%q],"score":%.2f,"%s":%q}`,
				rng.IntN(1_000_000), sentence(rng, 2), word(rng), word(rng), rng.IntN(2) == 0,
				word(rng), word(rng), rng.Float64()*100, word(rng), sentence(rng, 1+rng.IntN(6)))
		}
		fmt.Fprintf(&b, `],"meta":{"page":%d,"total":%d}}`, rng.IntN(50), rng.IntN(100_000))
	case bodyHTML:
		fmt.Fprintf(&b, `<!DOCTYPE html><html><head><title>%s</title></head><body>`, sentence(rng, 3))
		if plant != "" {
			fmt.Fprintf(&b, `<p>%s</p>`, plant)
		}
		for b.Len() < size {
			fmt.Fprintf(&b, `<div class="%s-%s"><h2>%s</h2><p>%s</p><a href="/%s/%s">%s</a></div>`,
				word(rng), word(rng), sentence(rng, 3), sentence(rng, 12+rng.IntN(20)), word(rng), word(rng), word(rng))
		}
		b.WriteString(`</body></html>`)
	case bodyXML:
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><root>`)
		for b.Len() < size {
			fmt.Fprintf(&b, `<item id="%d"><name>%s</name><value>%s</value></item>`, rng.IntN(100_000), word(rng), sentence(rng, 4))
		}
		b.WriteString(`</root>`)
	case bodyJS:
		for b.Len() < size {
			fmt.Fprintf(&b, `function %s%s(a,b){var %s="%s";return a.%s(b)+%d;}`,
				word(rng), capitalize(word(rng)), word(rng), sentence(rng, 3), word(rng), rng.IntN(1000))
		}
	case bodyCSS:
		for b.Len() < size {
			fmt.Fprintf(&b, `.%s-%s{color:#%06x;margin:%dpx %dpx;display:%s}`,
				word(rng), word(rng), rng.IntN(0xffffff), rng.IntN(32), rng.IntN(32), []string{"block", "flex", "grid", "none"}[rng.IntN(4)])
		}
	default:
		if plant != "" {
			b.WriteString(plant + " ")
		}
		for b.Len() < size {
			b.WriteString(sentence(rng, 8+rng.IntN(10)))
			b.WriteString(".\n")
		}
	}
	return b.Bytes()
}

// binaryBody returns size bytes of random data behind a magic number, so it
// is not valid UTF-8 text.
func binaryBody(rng *rand.Rand, magic string, size int) []byte {
	body := make([]byte, max(size, len(magic)+8))
	for i := 0; i < len(body); i += 8 {
		v := rng.Uint64()
		for j := 0; j < 8 && i+j < len(body); j++ {
			body[i+j] = byte(v >> (8 * j))
		}
	}
	copy(body, magic)
	// A NUL byte keeps the body binary however the random bytes fell.
	body[len(magic)] = 0
	return body
}

// encodeBody applies the content codings in header order, as a server does.
func encodeBody(body []byte, codings []string) []byte {
	for _, coding := range codings {
		var b bytes.Buffer
		switch coding {
		case "gzip":
			w := gzip.NewWriter(&b)
			w.Write(body)
			w.Close()
		case "deflate":
			w, _ := flate.NewWriter(&b, flate.DefaultCompression)
			w.Write(body)
			w.Close()
		case "zstd":
			w, _ := zstd.NewWriter(&b)
			w.Write(body)
			w.Close()
		default:
			panic("unknown coding " + coding)
		}
		body = b.Bytes()
	}
	return body
}

func hexString(rng *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[rng.IntN(16)]
	}
	return string(b)
}

func uuidString(rng *rand.Rand) string {
	h := hexString(rng, 32)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func itoa(n int) string { return strconv.Itoa(n) }

// capitalize upper-cases the first letter of an ASCII word.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
