package main

import (
	"net/http"
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

func TestCheckIdentity(t *testing.T) {
	const configuredKey = "AKIATEST"

	sigV4 := func(key string) string {
		return "AWS4-HMAC-SHA256 Credential=" + key + "/20260420/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc123"
	}

	cases := []struct {
		name   string
		cfg    AuthConfig
		header string
		want   authResult
	}{
		{"disabled no header", AuthConfig{}, "", authNotRequired},
		{"disabled with garbage", AuthConfig{}, "whatever", authNotRequired},
		{"enabled no header", AuthConfig{AccessKey: configuredKey}, "", authMissing},
		{"enabled Basic scheme", AuthConfig{AccessKey: configuredKey}, "Basic Zm9vOmJhcg==", authMalformed},
		{"enabled SigV4 no Credential", AuthConfig{AccessKey: configuredKey}, "AWS4-HMAC-SHA256 SignedHeaders=host, Signature=abc", authMalformed},
		{"enabled correct key", AuthConfig{AccessKey: configuredKey}, sigV4(configuredKey), authOK},
		{"enabled wrong key", AuthConfig{AccessKey: configuredKey}, sigV4("AKIAWRONG"), authWrongKey},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "http://example/bucket/key", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := tc.cfg.checkIdentity(r); got != tc.want {
				t.Errorf("checkIdentity() = %v, want %v", got, tc.want)
			}
		})
	}
}
