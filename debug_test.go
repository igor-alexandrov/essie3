package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
