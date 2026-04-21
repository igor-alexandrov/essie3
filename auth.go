package main

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
