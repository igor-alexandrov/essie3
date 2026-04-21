package main

import (
	"testing"
)

func TestAuthConfig_Enabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  AuthConfig
		want bool
	}{
		{"empty key disables auth", AuthConfig{}, false},
		{"blank key disables auth", AuthConfig{AccessKey: ""}, false},
		{"non-empty key enables auth", AuthConfig{AccessKey: "AKIATEST"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
