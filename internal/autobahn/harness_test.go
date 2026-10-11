package autobahn

import "testing"

func TestCheckProxied(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		wantErr       bool
	}{
		{"a suite that recorded its first connection passes", "", "b", false},
		{"a suite that recorded a newer connection passes", "a", "b", false},
		{"a suite that recorded no connection fails", "", "", true},
		{"a suite that recorded nothing new fails", "a", "a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkProxied(tt.before, tt.after); (err != nil) != tt.wantErr {
				t.Fatalf("\nwanted error:\n%v\ngot:\n%v", tt.wantErr, err)
			}
		})
	}
}
