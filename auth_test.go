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

func TestAuthorize(t *testing.T) {
	const configuredKey = "AKIATEST"
	cfg := AuthConfig{AccessKey: configuredKey}

	sigV4 := func(key string) string {
		return "AWS4-HMAC-SHA256 Credential=" + key + "/20260420/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc"
	}

	type want struct {
		allow  bool
		status int
		code   string
	}

	cases := []struct {
		name   string
		cfg    AuthConfig
		header string
		op     op
		acl    string
		want   want
	}{
		{"disabled always allows write", AuthConfig{}, "", opWrite, "", want{allow: true}},
		{"disabled always allows read", AuthConfig{}, "", opRead, "", want{allow: true}},

		{"enabled authed write", cfg, sigV4(configuredKey), opWrite, "", want{allow: true}},
		{"enabled authed read", cfg, sigV4(configuredKey), opRead, "", want{allow: true}},

		{"enabled missing header write", cfg, "", opWrite, "", want{status: 403, code: "AccessDenied"}},
		{"enabled missing header read private", cfg, "", opRead, "private", want{status: 403, code: "AccessDenied"}},
		{"enabled missing header read empty ACL", cfg, "", opRead, "", want{status: 403, code: "AccessDenied"}},
		{"enabled missing header read public", cfg, "", opRead, "public-read", want{allow: true}},

		{"enabled wrong key write", cfg, sigV4("AKIAWRONG"), opWrite, "", want{status: 403, code: "InvalidAccessKeyId"}},
		{"enabled wrong key read public", cfg, sigV4("AKIAWRONG"), opRead, "public-read", want{status: 403, code: "InvalidAccessKeyId"}},

		{"enabled malformed write", cfg, "Basic foo", opWrite, "", want{status: 400, code: "InvalidArgument"}},
		{"enabled malformed read public", cfg, "Basic foo", opRead, "public-read", want{status: 400, code: "InvalidArgument"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "http://example/bucket/key", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			got := tc.cfg.authorize(r, tc.op, tc.acl)
			if tc.want.allow {
				if got != nil {
					t.Fatalf("authorize = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("authorize = nil, want denial")
			}
			if got.status != tc.want.status {
				t.Errorf("status = %d, want %d", got.status, tc.want.status)
			}
			if got.code != tc.want.code {
				t.Errorf("code = %q, want %q", got.code, tc.want.code)
			}
		})
	}
}
