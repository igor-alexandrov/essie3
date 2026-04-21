package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
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

// sigV4HeaderForKey returns a syntactically valid SigV4 Authorization
// header. The signature is never checked, only the Credential key.
func sigV4HeaderForKey(key string) string {
	return "AWS4-HMAC-SHA256 Credential=" + key + "/20260420/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc123"
}

func TestHandlerAuth_Writes(t *testing.T) {
	const key = "AKIATEST"
	srv := testServerWithAuth(t, AuthConfig{AccessKey: key})
	defer srv.Close()

	// PUT without auth → 403 AccessDenied
	req, _ := http.NewRequest("PUT", srv.URL+"/mybucket/k", bytes.NewReader([]byte("hello")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("PUT unauth status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<Code>AccessDenied</Code>") {
		t.Fatalf("PUT unauth body = %s, want AccessDenied", body)
	}

	// PUT with wrong key → 403 InvalidAccessKeyId
	req, _ = http.NewRequest("PUT", srv.URL+"/mybucket/k", bytes.NewReader([]byte("hello")))
	req.Header.Set("Authorization", sigV4HeaderForKey("AKIAWRONG"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("PUT wrong-key status = %d, want 403", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<Code>InvalidAccessKeyId</Code>") {
		t.Fatalf("PUT wrong-key body = %s, want InvalidAccessKeyId", body)
	}

	// PUT with right key → 200 (after creating bucket)
	makeBucket, _ := http.NewRequest("PUT", srv.URL+"/mybucket", nil)
	makeBucket.Header.Set("Authorization", sigV4HeaderForKey(key))
	http.DefaultClient.Do(makeBucket)

	req, _ = http.NewRequest("PUT", srv.URL+"/mybucket/k", bytes.NewReader([]byte("hello")))
	req.Header.Set("Authorization", sigV4HeaderForKey(key))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("PUT authed status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE without auth → 403
	req, _ = http.NewRequest("DELETE", srv.URL+"/mybucket/k", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("DELETE unauth status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// COPY without auth → 403
	req, _ = http.NewRequest("PUT", srv.URL+"/mybucket/copy", nil)
	req.Header.Set("x-amz-copy-source", "/mybucket/k")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("COPY unauth status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// POST form without auth → 403
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("key", "uploads/f.txt")
	fw, _ := mw.CreateFormFile("file", "f.txt")
	fw.Write([]byte("hi"))
	mw.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/mybucket", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("POST unauth status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHandlerAuth_Reads(t *testing.T) {
	const key = "AKIATEST"
	srv := testServerWithAuth(t, AuthConfig{AccessKey: key})
	defer srv.Close()

	auth := func(req *http.Request) { req.Header.Set("Authorization", sigV4HeaderForKey(key)) }

	// Setup: create bucket, put one private object and one public-read object.
	mb, _ := http.NewRequest("PUT", srv.URL+"/b", nil)
	auth(mb)
	http.DefaultClient.Do(mb)

	priv, _ := http.NewRequest("PUT", srv.URL+"/b/private.txt", bytes.NewReader([]byte("secret")))
	auth(priv)
	http.DefaultClient.Do(priv)

	pub, _ := http.NewRequest("PUT", srv.URL+"/b/public.txt", bytes.NewReader([]byte("hello")))
	auth(pub)
	pub.Header.Set("x-amz-acl", "public-read")
	http.DefaultClient.Do(pub)

	// GET private unauth → 403
	req, _ := http.NewRequest("GET", srv.URL+"/b/private.txt", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("GET private unauth = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// GET public-read unauth → 200
	req, _ = http.NewRequest("GET", srv.URL+"/b/public.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("GET public unauth = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Errorf("GET public body = %q, want %q", body, "hello")
	}

	// GET public-read with WRONG key → 403 InvalidAccessKeyId (broken client not demoted to anonymous)
	req, _ = http.NewRequest("GET", srv.URL+"/b/public.txt", nil)
	req.Header.Set("Authorization", sigV4HeaderForKey("AKIAWRONG"))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("GET public wrong-key = %d, want 403", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<Code>InvalidAccessKeyId</Code>") {
		t.Errorf("GET public wrong-key body = %s, want InvalidAccessKeyId", body)
	}

	// HEAD private unauth → 403
	req, _ = http.NewRequest("HEAD", srv.URL+"/b/private.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("HEAD private unauth = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// HEAD public unauth → 200
	req, _ = http.NewRequest("HEAD", srv.URL+"/b/public.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("HEAD public unauth = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// GET nonexistent unauth → 403 (masks existence — same as private)
	req, _ = http.NewRequest("GET", srv.URL+"/b/missing.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("GET missing unauth = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}
