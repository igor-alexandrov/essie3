package main

import (
	"net/http"
	"strconv"
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
	return parseByteRange(spec, totalLen)
}

// parseByteRange parses a single byte-range spec (the part after
// "bytes="), evaluates it against totalLen, and returns the outcome.
// On malformed input it returns serveFull=true so the caller falls
// through to a full 200 response (RFC 9110 §14.2.1).
func parseByteRange(spec string, totalLen int64) rangeOutcome {
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return rangeOutcome{serveFull: true}
	}
	startStr := spec[:dash]
	endStr := spec[dash+1:]

	if startStr == "" && endStr == "" {
		return rangeOutcome{serveFull: true}
	}

	if totalLen == 0 {
		return rangeOutcome{} // unsatisfiable
	}

	// Suffix form: -N
	if startStr == "" {
		n, ok := parseInt(endStr)
		if !ok {
			return rangeOutcome{serveFull: true}
		}
		if n <= 0 {
			return rangeOutcome{} // unsatisfiable
		}
		start := totalLen - n
		if start < 0 {
			start = 0
		}
		return rangeOutcome{bounds: &byteRange{start: start, end: totalLen - 1}}
	}

	start, ok := parseInt(startStr)
	if !ok {
		return rangeOutcome{serveFull: true}
	}
	if start >= totalLen {
		return rangeOutcome{} // unsatisfiable
	}

	// Open-ended form: N-
	if endStr == "" {
		return rangeOutcome{bounds: &byteRange{start: start, end: totalLen - 1}}
	}

	// Full form: N-M
	end, ok := parseInt(endStr)
	if !ok {
		return rangeOutcome{serveFull: true}
	}
	if end < start {
		return rangeOutcome{} // unsatisfiable
	}
	if end >= totalLen {
		end = totalLen - 1
	}
	return rangeOutcome{bounds: &byteRange{start: start, end: end}}
}

// parseInt parses a non-negative decimal integer. Returns (0, false)
// on empty input, negative sign, or any non-digit character.
func parseInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
