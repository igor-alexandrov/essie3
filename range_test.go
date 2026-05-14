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

func TestEvaluateRange_FullSpec(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		totalLen  int64
		wantFull  bool
		wantStart int64
		wantEnd   int64
		wantSat   bool // true if unsatisfiable (bounds=nil and serveFull=false)
	}{
		{"N-M valid", "bytes=10-19", 100, false, 10, 19, false},
		{"N-M single byte", "bytes=0-0", 1, false, 0, 0, false},
		{"N-M M past end clamps", "bytes=50-500", 100, false, 50, 99, false},
		{"N-M N>=totalLen unsatisfiable", "bytes=100-200", 100, false, 0, 0, true},
		{"N-M N>M unsatisfiable", "bytes=5-3", 100, false, 0, 0, true},
		{"N- open-ended valid", "bytes=10-", 100, false, 10, 99, false},
		{"N- N>=totalLen unsatisfiable", "bytes=100-", 100, false, 0, 0, true},
		{"-N suffix valid", "bytes=-10", 100, false, 90, 99, false},
		{"-N suffix exceeds whole body", "bytes=-1000", 100, false, 0, 99, false},
		{"-N suffix zero unsatisfiable", "bytes=-0", 100, false, 0, 0, true},
		{"empty totalLen unsatisfiable", "bytes=0-10", 0, false, 0, 0, true},
		{"malformed empty spec serves full", "bytes=", 100, true, 0, 0, false},
		{"malformed letters serves full", "bytes=abc", 100, true, 0, 0, false},
		{"malformed both sides empty serves full", "bytes=-", 100, true, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/bucket/key", nil)
			req.Header.Set("Range", tc.header)

			out := evaluateRange(req, tc.totalLen, `"etag"`)

			if out.serveFull != tc.wantFull {
				t.Fatalf("serveFull = %v, want %v", out.serveFull, tc.wantFull)
			}
			if tc.wantFull {
				return
			}
			if tc.wantSat {
				if out.bounds != nil {
					t.Fatalf("bounds = %+v, want nil (unsatisfiable)", out.bounds)
				}
				return
			}
			if out.bounds == nil {
				t.Fatalf("bounds = nil, want {%d, %d}", tc.wantStart, tc.wantEnd)
			}
			if out.bounds.start != tc.wantStart || out.bounds.end != tc.wantEnd {
				t.Fatalf("bounds = {%d, %d}, want {%d, %d}",
					out.bounds.start, out.bounds.end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}
