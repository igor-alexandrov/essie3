package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// AuthConfig controls optional request authentication for essie3. When
// AccessKey is empty, auth is disabled and every request is served as-is
// (the default, matching the pre-auth behavior). When AccessKey is set,
// incoming requests must present that key in their SigV4 Authorization
// header. The server does NOT validate SigV4 signatures — only the
// Credential access-key portion is compared.
type AuthConfig struct {
	AccessKey      string
	FallbackPublic bool
}

// Enabled reports whether auth is active.
func (c AuthConfig) Enabled() bool {
	return c.AccessKey != ""
}

type authResult int

const (
	authNotRequired authResult = iota // auth is disabled; don't look at the request
	authOK                            // auth enabled and the presented key matches
	authMissing                       // auth enabled but no Authorization header
	authMalformed                     // auth enabled, header present but not parseable SigV4
	authWrongKey                      // auth enabled, SigV4 parseable but key doesn't match
)

const sigV4Prefix = "AWS4-HMAC-SHA256 "

// checkIdentity examines the Authorization header and returns an
// authResult describing whether the request is authenticated. It never
// validates the signature — only the Credential access-key portion.
func (c AuthConfig) checkIdentity(r *http.Request) authResult {
	if !c.Enabled() {
		return authNotRequired
	}

	header := r.Header.Get("Authorization")
	if header == "" {
		return authMissing
	}
	if !strings.HasPrefix(header, sigV4Prefix) {
		return authMalformed
	}

	// Parameters are comma-separated after the scheme prefix:
	//   Credential=<key>/<date>/<region>/s3/aws4_request, SignedHeaders=..., Signature=...
	params := strings.TrimPrefix(header, sigV4Prefix)
	for _, p := range strings.Split(params, ",") {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "Credential=") {
			continue
		}
		cred := strings.TrimPrefix(p, "Credential=")
		slash := strings.IndexByte(cred, '/')
		if slash <= 0 {
			return authMalformed
		}
		presented := cred[:slash]
		if subtle.ConstantTimeCompare([]byte(presented), []byte(c.AccessKey)) == 1 {
			return authOK
		}
		return authWrongKey
	}
	return authMalformed
}
