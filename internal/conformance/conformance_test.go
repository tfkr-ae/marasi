package conformance

import "testing"

func TestCheckProxied(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		wantErr       bool
	}{
		{"should pass a target that recorded its first item", "", "b", false},
		{"should pass a target that recorded a newer item", "a", "b", false},
		{"should fail a target that recorded no item", "", "", true},
		{"should fail a target that recorded nothing new", "a", "a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkProxied(tt.before, tt.after); (err != nil) != tt.wantErr {
				t.Fatalf("\nwanted error:\n%v\ngot:\n%v", tt.wantErr, err)
			}
		})
	}
}

func TestCaseID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		approach Approach
		want     string
	}{
		{"should append the approach", "chunked-trailers", ApproachConnectTLS, "chunked-trailers@connect-tls"},
		{"should keep a bare ID with no approach", "6.4.1", ApproachNone, "6.4.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CaseID(tt.id, tt.approach)
			if got != tt.want {
				t.Fatalf("\nwanted:\n%s\ngot:\n%s", tt.want, got)
			}
			if approach := CaseApproach(got); approach != tt.approach {
				t.Fatalf("\nwanted approach:\n%q\ngot:\n%q", tt.approach, approach)
			}
		})
	}
}
