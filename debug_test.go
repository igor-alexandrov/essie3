package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDebugResponseWriter_DefaultStatusIs200(t *testing.T) {
	rec := httptest.NewRecorder()
	drw := newDebugResponseWriter(rec)

	// No WriteHeader call before Write — Go's net/http treats this as 200 OK.
	if _, err := drw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if drw.status != http.StatusOK {
		t.Errorf("status = %d, want %d", drw.status, http.StatusOK)
	}
	if drw.bytes != 5 {
		t.Errorf("bytes = %d, want 5", drw.bytes)
	}
}

func TestDebugResponseWriter_CapturesExplicitStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	drw := newDebugResponseWriter(rec)

	drw.WriteHeader(http.StatusForbidden)
	if _, err := drw.Write([]byte("denied")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if drw.status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", drw.status, http.StatusForbidden)
	}
	if drw.bytes != 6 {
		t.Errorf("bytes = %d, want 6", drw.bytes)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("underlying recorder code = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Body.String(); got != "denied" {
		t.Errorf("underlying body = %q, want %q", got, "denied")
	}
}

func TestDebugResponseWriter_HeaderDelegates(t *testing.T) {
	rec := httptest.NewRecorder()
	drw := newDebugResponseWriter(rec)

	drw.Header().Set("X-Foo", "bar")
	if got := rec.Header().Get("X-Foo"); got != "bar" {
		t.Errorf("underlying X-Foo = %q, want %q", got, "bar")
	}
}

func TestFormatRequest_BasicGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	req.Header.Set("Host", "localhost:9000")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIATEST/x/y/s3/aws4_request, Signature=abc")

	got := formatRequest(req)

	want := "--> GET /bucket/key\n" +
		"    Authorization: AWS4-HMAC-SHA256 Credential=AKIATEST/x/y/s3/aws4_request, Signature=abc\n" +
		"    Host: localhost:9000\n"
	if got != want {
		t.Errorf("formatRequest mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatRequest_PreservesQueryString(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/bucket?list-type=2&prefix=foo", nil)

	got := formatRequest(req)

	if !strings.HasPrefix(got, "--> GET /bucket?list-type=2&prefix=foo\n") {
		t.Errorf("expected request line to include query string, got:\n%s", got)
	}
}

func TestFormatRequest_MultiValueHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", nil)
	req.Header.Add("X-Amz-Meta-Tag", "first")
	req.Header.Add("X-Amz-Meta-Tag", "second")

	got := formatRequest(req)

	if !strings.Contains(got, "    X-Amz-Meta-Tag: first\n") {
		t.Errorf("expected first value line, got:\n%s", got)
	}
	if !strings.Contains(got, "    X-Amz-Meta-Tag: second\n") {
		t.Errorf("expected second value line, got:\n%s", got)
	}
}

func TestFormatRequest_HeadersAreSorted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	req.Header.Set("Zebra", "z")
	req.Header.Set("Apple", "a")
	req.Header.Set("Mango", "m")

	got := formatRequest(req)

	appleIdx := strings.Index(got, "Apple:")
	mangoIdx := strings.Index(got, "Mango:")
	zebraIdx := strings.Index(got, "Zebra:")
	if !(appleIdx < mangoIdx && mangoIdx < zebraIdx) {
		t.Errorf("headers not sorted alphabetically:\n%s", got)
	}
}

func TestFormatResponse_BasicOK(t *testing.T) {
	rec := httptest.NewRecorder()
	drw := newDebugResponseWriter(rec)
	drw.Header().Set("Etag", `"abc"`)
	drw.WriteHeader(http.StatusOK)
	drw.Write([]byte("hello"))

	got := formatResponse(drw, 12300*time.Microsecond) // 12.3ms

	want := "<-- 200 OK (12.3ms, 5 bytes)\n" +
		"    Etag: \"abc\"\n"
	if got != want {
		t.Errorf("formatResponse mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatResponse_ForbiddenWithSortedHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	drw := newDebugResponseWriter(rec)
	drw.Header().Set("X-Amz-Request-Id", "req-1")
	drw.Header().Set("Content-Type", "application/xml")
	drw.WriteHeader(http.StatusForbidden)
	drw.Write([]byte("<Error/>"))

	got := formatResponse(drw, 2*time.Millisecond)

	want := "<-- 403 Forbidden (2ms, 8 bytes)\n" +
		"    Content-Type: application/xml\n" +
		"    X-Amz-Request-Id: req-1\n"
	if got != want {
		t.Errorf("formatResponse mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
