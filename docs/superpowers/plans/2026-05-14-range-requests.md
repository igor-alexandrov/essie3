# Range Request Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add HTTP `Range` request support to essie3's GET/HEAD endpoints (real objects and fallback placeholders), with `If-Range` ETag matching, S3-shaped 416 errors, and `Accept-Ranges: bytes` on all object/fallback responses.

**Architecture:** A new file `range.go` exports a pure function `evaluateRange(r *http.Request, totalLen int64, etag string) rangeOutcome` that returns one of three signals: serve full body, serve a 206 with bounds, or return 416. `handler.go`'s four GET/HEAD code paths (real-object + fallback × GET + HEAD) call this function once after auth and branch on the outcome. A new helper `writeInvalidRange` in `xml.go` writes the S3-shaped 416. Storage is unchanged — slicing happens on the buffered body that `GetObject` already returns.

**Tech Stack:** Go 1.22, standard library only (`net/http`, `strconv`, `strings`).

**Spec:** [`docs/superpowers/specs/2026-05-14-range-requests-design.md`](../specs/2026-05-14-range-requests-design.md)

---

## File Structure

- **Create** `range.go` — `byteRange`, `rangeOutcome`, `evaluateRange`.
- **Create** `range_test.go` — unit tests for `evaluateRange`.
- **Modify** `xml.go` — add `writeInvalidRange` helper.
- **Modify** `handler.go` — Range branch in `handleGetObject` and `handleHeadObject` (both real-object and fallback paths in each); add `strconv` to the import block; tighten the existing `Content-Length` writes to use `strconv.FormatInt`.
- **Modify** `handler_test.go` — integration tests covering all outcomes.
- **Modify** `README.md` — short "Range requests" subsection.

---

### Task 1: `evaluateRange` early-out cases

The first slice of `evaluateRange`: the three cases that fall through to "serve full body" without ever parsing a single spec — no `Range` header, non-`bytes=` unit, multi-range. Locks in the public types (`byteRange`, `rangeOutcome`) and the function signature.

**Files:**
- Create: `range.go`
- Create: `range_test.go`

- [ ] **Step 1: Write the failing tests**

Create `range_test.go` with initial contents:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange -v`
Expected: compile failure — `undefined: evaluateRange`.

- [ ] **Step 3: Create `range.go`**

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange -v`
Expected: PASS for all three subtests.

- [ ] **Step 5: Commit**

```bash
git add range.go range_test.go
git commit -m "Add evaluateRange skeleton handling no-Range, unknown-unit, multi-range"
```

---

### Task 2: parse single byte-range spec

Add the actual parsing for the three forms (`N-M`, `N-`, `-N`), the unsatisfiable cases, and the `end` clamp. After this task, `evaluateRange` produces correct outcomes for every well-formed single-range request, but does not yet honor `If-Range` (Task 3 adds that).

**Files:**
- Modify: `range.go`
- Modify: `range_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `range_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange_FullSpec -v`
Expected: most subtests FAIL — current `evaluateRange` returns `serveFull=true` for everything, so any case where `wantFull=false` will fail with `serveFull = true, want false`.

- [ ] **Step 3: Implement the parser**

In `range.go`, add `"strconv"` to the import block:

```go
import (
	"net/http"
	"strconv"
	"strings"
)
```

Replace the final `return rangeOutcome{serveFull: true}` line of `evaluateRange` with a call to a new `parseByteRange` helper, and append that helper plus a small `parseInt` wrapper to the file:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange -v`
Expected: PASS for all `TestEvaluateRange_*` subtests, including all 14 `TestEvaluateRange_FullSpec` cases and the three earlier ones from Task 1.

- [ ] **Step 5: Commit**

```bash
git add range.go range_test.go
git commit -m "Parse single byte-range spec with clamp and unsatisfiable handling"
```

---

### Task 3: `If-Range` ETag matching

Honor the `If-Range` header when present. If its literal value (including surrounding quotes) matches the object's ETag, parse the Range as usual; otherwise fall through to a full-body 200. Date-shaped values are treated as non-match (we don't parse dates).

**Files:**
- Modify: `range.go`
- Modify: `range_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `range_test.go`:

```go
func TestEvaluateRange_IfRangeMatchesETag(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)
	req.Header.Set("Range", "bytes=10-19")
	req.Header.Set("If-Range", `"abc"`)

	out := evaluateRange(req, 100, `"abc"`)

	if out.serveFull {
		t.Fatalf("serveFull = true, want false (If-Range matched)")
	}
	if out.bounds == nil || out.bounds.start != 10 || out.bounds.end != 19 {
		t.Fatalf("bounds = %+v, want {10, 19}", out.bounds)
	}
}

func TestEvaluateRange_IfRangeMismatchETag(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)
	req.Header.Set("Range", "bytes=10-19")
	req.Header.Set("If-Range", `"wrong"`)

	out := evaluateRange(req, 100, `"abc"`)

	if !out.serveFull {
		t.Errorf("serveFull = false, want true (If-Range mismatched)")
	}
}

func TestEvaluateRange_IfRangeDateTreatedAsMismatch(t *testing.T) {
	req := httptest.NewRequest("GET", "/bucket/key", nil)
	req.Header.Set("Range", "bytes=10-19")
	req.Header.Set("If-Range", "Wed, 21 Oct 2015 07:28:00 GMT")

	out := evaluateRange(req, 100, `"abc"`)

	if !out.serveFull {
		t.Errorf("serveFull = false, want true (date-shaped If-Range is mismatch)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange_IfRange -v`
Expected: `TestEvaluateRange_IfRangeMismatchETag` and `TestEvaluateRange_IfRangeDateTreatedAsMismatch` FAIL (current code ignores `If-Range`, so the Range is honored when it shouldn't be). `TestEvaluateRange_IfRangeMatchesETag` passes by coincidence.

- [ ] **Step 3: Implement `If-Range` check**

In `range.go`, in `evaluateRange`, insert the If-Range check between the multi-range guard and the `parseByteRange` call. The full updated `evaluateRange`:

```go
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
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		return rangeOutcome{serveFull: true}
	}
	return parseByteRange(spec, totalLen)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestEvaluateRange -v`
Expected: PASS for every `TestEvaluateRange_*` subtest in the file.

- [ ] **Step 5: Commit**

```bash
git add range.go range_test.go
git commit -m "Honor If-Range with ETag matching"
```

---

### Task 4: `writeInvalidRange` helper in `xml.go`

The S3-shaped 416 response. `Content-Range: bytes */<totalLen>` must be set before calling `writeXMLError` (which calls `WriteHeader`).

**Files:**
- Modify: `xml.go`
- Create: `xml_test.go`

- [ ] **Step 1: Write the failing test**

Create `xml_test.go` with the following contents:

```go
package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteInvalidRange(t *testing.T) {
	rec := httptest.NewRecorder()

	writeInvalidRange(rec, "mybucket", "photos/photo.jpg", 1000)

	if rec.Code != 416 {
		t.Errorf("status = %d, want 416", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */1000" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes */1000")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want %q", got, "application/xml")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<Code>InvalidRange</Code>") {
		t.Errorf("body missing <Code>InvalidRange</Code>:\n%s", body)
	}
	if !strings.Contains(body, "<BucketName>mybucket</BucketName>") {
		t.Errorf("body missing <BucketName>mybucket</BucketName>:\n%s", body)
	}
	if !strings.Contains(body, "<Key>photos/photo.jpg</Key>") {
		t.Errorf("body missing <Key>photos/photo.jpg</Key>:\n%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestWriteInvalidRange -v`
Expected: compile failure — `undefined: writeInvalidRange`.

- [ ] **Step 3: Implement `writeInvalidRange`**

In `xml.go`, append:

```go
// writeInvalidRange writes the S3-shaped 416 response for an
// unsatisfiable Range request. Content-Range must be set before
// writeXMLError calls WriteHeader.
func writeInvalidRange(w http.ResponseWriter, bucket, key string, totalLen int64) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalLen))
	writeXMLError(w, http.StatusRequestedRangeNotSatisfiable,
		"InvalidRange",
		"The requested range is not satisfiable",
		bucket, key)
}
```

(`fmt` and `net/http` are already imported in `xml.go`; no new imports needed.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestWriteInvalidRange -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add xml.go xml_test.go
git commit -m "Add writeInvalidRange helper for S3-shaped 416 responses"
```

---

### Task 5: Wire Range support into `handler.go`

Modify all four GET/HEAD call sites (real-object + fallback in each of `handleGetObject` and `handleHeadObject`) to call `evaluateRange` after auth, set `Accept-Ranges: bytes`, and branch on the outcome. The existing non-Range request behavior must be unchanged except for the additive `Accept-Ranges` header.

**Files:**
- Modify: `handler.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing integration tests**

Append to `handler_test.go`:

```go
func TestHandler_AcceptRangesOnGet(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	resp, err := http.Get(srv.URL + "/b/k.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}

func TestHandler_AcceptRangesOnHead(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("HEAD", srv.URL+"/b/k.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}

func TestHandler_GetWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-4/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-4/11")
	}
	if got := resp.Header.Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q, want %q", got, "5")
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestHandler_GetWithSuffixRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 6-10/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 6-10/11")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("body = %q, want %q", body, "world")
	}
}

func TestHandler_GetWithOpenEndedRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=6-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 6-10/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 6-10/11")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("body = %q, want %q", body, "world")
	}
}

func TestHandler_GetUnsatisfiableRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=1000-2000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 416 {
		t.Fatalf("status = %d, want 416", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes */11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes */11")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidRange</Code>") {
		t.Errorf("body missing InvalidRange code:\n%s", body)
	}
}

func TestHandler_HeadWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("HEAD", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-4/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-4/11")
	}
	if got := resp.Header.Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q, want %q", got, "5")
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %q", body)
	}
}

func TestHandler_IfRangeMatches(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	putResp, _ := http.DefaultClient.Do(req)
	etag := putResp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("PUT did not return ETag")
	}

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("If-Range", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206 (If-Range matched)", resp.StatusCode)
	}
}

func TestHandler_IfRangeMismatch(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("If-Range", `"wrong-etag"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (If-Range mismatched)", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "" {
		t.Errorf("Content-Range = %q, want empty (full response)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want full body", body)
	}
}

func TestHandler_GetFallbackWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// First, fetch the full fallback to learn its byte length.
	fullResp, err := http.Get(srv.URL + "/bucket/missing/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	fullBody, _ := io.ReadAll(fullResp.Body)
	fullResp.Body.Close()
	if len(fullBody) < 10 {
		t.Fatalf("fallback body too short (%d bytes) for this test", len(fullBody))
	}

	req, _ := http.NewRequest("GET", srv.URL+"/bucket/missing/photo.jpg", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	wantCR := fmt.Sprintf("bytes 0-9/%d", len(fullBody))
	if got := resp.Header.Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, fullBody[0:10]) {
		t.Errorf("body slice mismatch")
	}
}

func TestHandler_HeadFallbackHasAcceptRanges(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/bucket/missing/photo.jpg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}
```

If `fmt` is not already imported in `handler_test.go`, add it. (`bytes` is already imported per the existing test file.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_(AcceptRanges|GetWith|GetUnsatisfiable|HeadWith|IfRange|GetFallbackWith|HeadFallback)" -v`
Expected: most subtests FAIL — current handler doesn't set `Accept-Ranges`, doesn't return 206, and ignores `Range` entirely.

- [ ] **Step 3: Modify `handler.go`**

Add `"strconv"` to the import block.

Replace the body of `handleGetObject` with the version below. The changes versus the current implementation are: shared `Accept-Ranges` setter; both real-object and fallback paths call `evaluateRange` and branch on it; `Content-Length` writes use `strconv.FormatInt`.

```go
func (h *Handler) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, objErr := h.storage.GetObject(bucket, key)

	var acl string
	if objErr == nil {
		acl = obj.Meta.ACL
	}

	authErr := h.auth.authorize(r, opRead, acl)

	if objErr != nil {
		if p := h.fallback.Select(key); p != nil {
			if authErr != nil && !h.auth.FallbackPublic {
				writeAuthError(w, authErr, bucket, key)
				return
			}
			totalLen := int64(len(p.Body))
			out := evaluateRange(r, totalLen, "")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
			switch {
			case out.serveFull:
				w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
				w.WriteHeader(http.StatusOK)
				w.Write(p.Body)
			case out.bounds != nil:
				s, e := out.bounds.start, out.bounds.end
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
				w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(p.Body[s : e+1])
			default:
				writeInvalidRange(w, bucket, key, totalLen)
			}
			return
		}
		if authErr != nil {
			writeAuthError(w, authErr, bucket, key)
			return
		}
		writeNoSuchKey(w, bucket, key)
		return
	}

	if authErr != nil {
		writeAuthError(w, authErr, bucket, key)
		return
	}

	totalLen := int64(len(obj.Body))
	out := evaluateRange(r, totalLen, obj.Meta.ETag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", obj.Meta.ContentType)
	w.Header().Set("ETag", obj.Meta.ETag)
	w.Header().Set("Last-Modified", obj.Meta.CreatedAt.UTC().Format(http.TimeFormat))
	if obj.Meta.ContentDisposition != "" {
		w.Header().Set("Content-Disposition", obj.Meta.ContentDisposition)
	}
	switch {
	case out.serveFull:
		w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
		w.WriteHeader(http.StatusOK)
		w.Write(obj.Body)
	case out.bounds != nil:
		s, e := out.bounds.start, out.bounds.end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
		w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(obj.Body[s : e+1])
	default:
		writeInvalidRange(w, bucket, key, totalLen)
	}
}
```

Replace the body of `handleHeadObject` with the version below. Same structure, no body writes:

```go
func (h *Handler) handleHeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, metaErr := h.storage.HeadObject(bucket, key)

	var acl string
	if metaErr == nil {
		acl = meta.ACL
	}

	authErr := h.auth.authorize(r, opRead, acl)

	if metaErr != nil {
		if p := h.fallback.Select(key); p != nil {
			if authErr != nil && !h.auth.FallbackPublic {
				writeAuthError(w, authErr, bucket, key)
				return
			}
			totalLen := int64(len(p.Body))
			out := evaluateRange(r, totalLen, "")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
			switch {
			case out.serveFull:
				w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
				w.WriteHeader(http.StatusOK)
			case out.bounds != nil:
				s, e := out.bounds.start, out.bounds.end
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
				w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
				w.WriteHeader(http.StatusPartialContent)
			default:
				writeInvalidRange(w, bucket, key, totalLen)
			}
			return
		}
		if authErr != nil {
			writeAuthError(w, authErr, bucket, key)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if authErr != nil {
		writeAuthError(w, authErr, bucket, key)
		return
	}

	totalLen := meta.ContentLength
	out := evaluateRange(r, totalLen, meta.ETag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.CreatedAt.UTC().Format(http.TimeFormat))
	switch {
	case out.serveFull:
		w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
		w.WriteHeader(http.StatusOK)
	case out.bounds != nil:
		s, e := out.bounds.start, out.bounds.end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
		w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	default:
		writeInvalidRange(w, bucket, key, totalLen)
	}
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_(AcceptRanges|GetWith|GetUnsatisfiable|HeadWith|IfRange|GetFallbackWith|HeadFallback)" -v`
Expected: PASS for every new subtest.

- [ ] **Step 5: Run the full suite to confirm no regressions**

Run: `cd /Users/igor/workspace/essie3 && go test ./...`
Expected: all existing tests still pass. The pre-existing `TestHandler_PutAndGetObject`, `TestHandler_HeadObject`, etc. assert on body / Content-Type / Content-Length / ETag — none of those values change for non-Range requests.

- [ ] **Step 6: Smoke-test manually**

Start the server in one terminal:

```bash
cd /Users/igor/workspace/essie3
PORT=9999 DATA_DIR=/tmp/essie3-range-smoke go run .
```

In another:

```bash
curl -X PUT http://localhost:9999/b
curl -X PUT --data-binary "hello world" -H "Content-Type: text/plain" http://localhost:9999/b/k.txt
curl -i http://localhost:9999/b/k.txt | grep -i "accept-ranges"     # → Accept-Ranges: bytes
curl -i -H "Range: bytes=0-4" http://localhost:9999/b/k.txt          # → 206, Content-Range: bytes 0-4/11, body: hello
curl -i -H "Range: bytes=-5"  http://localhost:9999/b/k.txt          # → 206, Content-Range: bytes 6-10/11, body: world
curl -i -H "Range: bytes=100-200" http://localhost:9999/b/k.txt      # → 416, Content-Range: bytes */11, XML body
```

Stop the server with Ctrl-C and clean up: `rm -rf /tmp/essie3-range-smoke`.

- [ ] **Step 7: Commit**

```bash
git add handler.go handler_test.go
git commit -m "Honor Range requests in GET and HEAD for objects and fallbacks"
```

---

### Task 6: Document Range support in the README

Add to the Features list and a short subsection after the Usage examples.

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add to the Features list**

In `README.md`, in the Features list (the bulleted list that begins with "- S3-style `PUT`, `GET`, ..."), add the following bullet immediately after the bullet that begins with "- Per-object metadata persisted as JSON sidecar files":

```
- HTTP `Range` requests (single-range) with `If-Range` ETag matching
  on objects and fallback placeholders
```

- [ ] **Step 2: Add a subsection after the Usage examples**

Find the heading line `## Auth (optional)` in `README.md`. Immediately **before** that line, insert:

```markdown
## Range requests

GET and HEAD on objects and fallback placeholders honor the
[HTTP `Range` header](https://datatracker.ietf.org/doc/html/rfc9110#section-14.2)
in its three single-range forms:

```sh
curl -H "Range: bytes=0-4"   http://localhost:9000/mybucket/photos/photo.jpg
curl -H "Range: bytes=1024-" http://localhost:9000/mybucket/photos/photo.jpg
curl -H "Range: bytes=-256"  http://localhost:9000/mybucket/photos/photo.jpg
```

Responses include `Accept-Ranges: bytes`. A satisfiable Range returns
`206 Partial Content` with `Content-Range: bytes <start>-<end>/<total>`
and the sliced body. An unsatisfiable Range returns
`416 Requested Range Not Satisfiable` with an S3-shaped XML body
(`<Code>InvalidRange</Code>`) and `Content-Range: bytes */<total>`.

`If-Range: "<etag>"` is honored against the object's ETag — if the
header matches, the Range is served; if it doesn't, the full body is
served as a 200 instead (so a client resuming an interrupted download
never merges bytes from a changed object). `If-Range` with a date
value is treated as a mismatch.

Multi-range requests (`Range: bytes=0-100, 200-300`) are not
supported; essie3 ignores them and serves the full body.

```

(Yes, the closing triple-backticks above are part of the inserted
block — the inserted Markdown contains its own fenced code block. Make
sure both fences are preserved.)

- [ ] **Step 3: Verify the markdown looks right**

Run: `grep -c '^|' README.md` — sanity check; the env-var table row count should be unchanged. Visually scan the new section for backtick balance.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "Document Range request support"
```

---

## Self-Review Notes

**Spec coverage:**
- `range.go` with `byteRange`, `rangeOutcome`, `evaluateRange` → Tasks 1, 2, 3.
- Early-out cases (no Range / unknown unit / multi-range) → Task 1.
- Parsing `N-M`, `N-`, `-N` with clamp and unsatisfiable → Task 2.
- `If-Range` ETag matching, date-as-mismatch → Task 3.
- `writeInvalidRange` helper in `xml.go` with `Content-Range: bytes */<total>` set before `WriteHeader` → Task 4.
- Wiring into all four call sites (handleGetObject real + fallback, handleHeadObject real + fallback) → Task 5.
- `Accept-Ranges: bytes` on all GET/HEAD object/fallback responses (200 and 206) → Task 5 (set before the `switch` in every branch).
- HEAD with Range returns 206 with no body → Task 5 (mirror of GET, no `Write`).
- `strconv.FormatInt` cleanup for the existing `Content-Length` writes → Task 5.
- README "Range requests" subsection → Task 6.
- Integration tests covering every outcome → Task 5.
- Unit tests covering the parser → Tasks 1, 2, 3.
- Unit test for `writeInvalidRange` → Task 4.

**Out of scope (confirmed not in plan):** multi-range (`multipart/byteranges`), streaming body slices, `If-Range` date matching, `If-Modified-Since` / `If-None-Match`.
