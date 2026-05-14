package main

import (
	"net/http/httptest"
	"testing"
)

func TestEvaluateRange_NoHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)

	out := evaluateRange(req, 100, `"etag"`)

	if !out.serveFull {
		t.Errorf("serveFull = false, want true")
	}
	if out.bounds != nil {
		t.Errorf("bounds = %+v, want nil", out.bounds)
	}
}

func TestEvaluateRange_UnknownUnit(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)
	req.Header.Set("Range", "items=0-10")

	out := evaluateRange(req, 100, `"etag"`)

	if !out.serveFull {
		t.Errorf("serveFull = false, want true")
	}
}

func TestEvaluateRange_MultiRange(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)
	req.Header.Set("Range", "bytes=0-10, 20-30")

	out := evaluateRange(req, 100, `"etag"`)

	if !out.serveFull {
		t.Errorf("serveFull = false, want true")
	}
}
