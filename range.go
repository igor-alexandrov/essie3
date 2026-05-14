package main

import (
	"net/http"
	"strings"
)

// byteRange is a closed [start, end] interval in bytes.
type byteRange struct {
	start, end int64
}

// rangeOutcome is the action the caller should take after evaluating a
// Range/If-Range pair. Exactly one of these three states applies:
//
//	serveFull=true                 → return 200 with the full body
//	serveFull=false, bounds != nil → return 206 with bounds
//	serveFull=false, bounds == nil → return 416
type rangeOutcome struct {
	serveFull bool
	bounds    *byteRange
}

// evaluateRange parses Range and If-Range against a representation of
// length totalLen and ETag etag, and returns the action the caller
// should take.
func evaluateRange(r *http.Request, totalLen int64, etag string) rangeOutcome {
	h := r.Header.Get("Range")
	if h == "" {
		return rangeOutcome{serveFull: true}
	}
	if !strings.HasPrefix(h, "bytes=") {
		return rangeOutcome{serveFull: true}
	}
	spec := strings.TrimPrefix(h, "bytes=")
	if strings.Contains(spec, ",") {
		return rangeOutcome{serveFull: true}
	}
	// Tasks 2 and 3 fill in the parsing and If-Range logic. Until they
	// land, any well-formed single-range request falls through to a
	// full-body response.
	return rangeOutcome{serveFull: true}
}
