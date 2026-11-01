// Package cvss reads CVSS 3.1 base vectors and works out their base score,
// as the FIRST CVSS v3.1 specification defines it.
package cvss

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Prefix starts every CVSS 3.1 vector.
const Prefix = "CVSS:3.1/"

// metric is one base metric: its abbreviation and the weight of each value.
type metric struct {
	name    string
	weights map[string]float64
}

// baseMetrics are the eight base metrics in the order a vector writes them.
// Privileges Required weighs more when the scope changes; see
// privilegesScopeChanged.
var baseMetrics = []metric{
	{"AV", map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}},
	{"AC", map[string]float64{"L": 0.77, "H": 0.44}},
	{"PR", map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}},
	{"UI", map[string]float64{"N": 0.85, "R": 0.62}},
	{"S", map[string]float64{"U": 0, "C": 0}},
	{"C", map[string]float64{"H": 0.56, "L": 0.22, "N": 0}},
	{"I", map[string]float64{"H": 0.56, "L": 0.22, "N": 0}},
	{"A", map[string]float64{"H": 0.56, "L": 0.22, "N": 0}},
}

// privilegesScopeChanged is the Privileges Required weight when Scope is C.
var privilegesScopeChanged = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}

// Base normalizes a CVSS 3.1 base vector and returns it with its base score.
// It trims the vector and adds Prefix when the eight base metrics come
// without it. Metrics may come in any order; the normalized vector writes
// them in the specification's order. A vector with another version, a
// missing, repeated or unknown metric, or a value a metric does not take is
// an error naming the problem.
func Base(vector string) (normalized string, score float64, err error) {
	body := strings.TrimSpace(vector)
	if version, rest, found := strings.Cut(body, "/"); found && strings.HasPrefix(version, "CVSS:") {
		if version+"/" != Prefix {
			return "", 0, fmt.Errorf("%s is not CVSS 3.1; write the vector as %sAV:N/AC:L/...", version, Prefix)
		}
		body = rest
	}
	values := make(map[string]string, len(baseMetrics))
	for part := range strings.SplitSeq(body, "/") {
		name, value, found := strings.Cut(part, ":")
		if !found || name == "" || value == "" {
			return "", 0, fmt.Errorf("%q is not a metric; write each as NAME:VALUE, as in AV:N", part)
		}
		if _, repeated := values[name]; repeated {
			return "", 0, fmt.Errorf("metric %s appears twice", name)
		}
		values[name] = value
	}
	weights := make(map[string]float64, len(baseMetrics))
	parts := make([]string, 0, len(baseMetrics))
	for _, m := range baseMetrics {
		value, ok := values[m.name]
		if !ok {
			return "", 0, fmt.Errorf("base metric %s is missing", m.name)
		}
		weight, ok := m.weights[value]
		if !ok {
			return "", 0, fmt.Errorf("%s:%s is not a value metric %s takes", m.name, value, m.name)
		}
		weights[m.name] = weight
		parts = append(parts, m.name+":"+value)
	}
	if len(values) > len(baseMetrics) {
		return "", 0, fmt.Errorf("%s is not a CVSS 3.1 base metric", firstExtra(body))
	}
	scopeChanged := values["S"] == "C"
	if scopeChanged {
		weights["PR"] = privilegesScopeChanged[values["PR"]]
	}
	return Prefix + strings.Join(parts, "/"), baseScore(weights, scopeChanged), nil
}

// firstExtra returns the first metric in body's order that is not a base
// metric, so the error does not depend on map order.
func firstExtra(body string) string {
	for part := range strings.SplitSeq(body, "/") {
		name, _, _ := strings.Cut(part, ":")
		if !slices.ContainsFunc(baseMetrics, func(m metric) bool { return m.name == name }) {
			return name
		}
	}
	return ""
}

// baseScore is the CVSS 3.1 base score equation (specification section 7.1).
func baseScore(w map[string]float64, scopeChanged bool) float64 {
	iss := 1 - (1-w["C"])*(1-w["I"])*(1-w["A"])
	impact := 6.42 * iss
	if scopeChanged {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	}
	if impact <= 0 {
		return 0
	}
	exploitability := 8.22 * w["AV"] * w["AC"] * w["PR"] * w["UI"]
	if scopeChanged {
		return roundUp(math.Min(1.08*(impact+exploitability), 10))
	}
	return roundUp(math.Min(impact+exploitability, 10))
}

// roundUp is the specification's Roundup (Appendix A): the smallest number,
// to one decimal place, that is equal to or higher than its input. It works
// in integers so floating-point error cannot push 4.0 up to 4.1.
func roundUp(value float64) float64 {
	scaled := int64(math.Round(value * 100000))
	if scaled%10000 == 0 {
		return float64(scaled) / 100000
	}
	return float64(scaled/10000+1) / 10
}
