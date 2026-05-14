# DEBUG Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in DEBUG mode (`ESSIE3_DEBUG=true`) that prints per-request method, path, headers, status code, response headers, byte count, and elapsed time to stderr — useful for debugging integration tests against essie3, especially auth-failure paths where the `Authorization` header itself is the thing under test.

**Architecture:** A new `debug.go` file holds an HTTP middleware `WithDebugLogging(http.Handler, io.Writer) http.Handler` plus an unexported `debugResponseWriter` that captures status and bytes-written, and two pure formatting helpers (`formatRequest`, `formatResponse`) that build the multi-line output blocks. `main.go` reads `ESSIE3_DEBUG` and wraps the core handler with the middleware only when enabled. The existing minimal per-request log line in `handler.go` is removed since its content is a strict subset of the DEBUG output. Output is serialized through a mutex-protected `*log.Logger` so concurrent requests cannot interleave their multi-line blocks.

**Tech Stack:** Go 1.22, standard library only (`net/http`, `net/http/httptest`, `log`, `bytes`, `strings`, `sort`, `time`, `io`).

**Spec:** [`docs/superpowers/specs/2026-05-14-debug-mode-design.md`](../specs/2026-05-14-debug-mode-design.md)

---

## File Structure

- **Create** `debug.go` — `WithDebugLogging`, unexported `debugResponseWriter`, `formatRequest`, `formatResponse`.
- **Create** `debug_test.go` — unit tests for `debugResponseWriter`, `formatRequest`, `formatResponse`, plus an end-to-end test that wraps a stub handler with the middleware and asserts captured output.
- **Modify** `handler.go` — remove the existing `log.Printf("%s %s", ...)` line in `ServeHTTP` (its content is a subset of DEBUG output; without removal, every request logs twice when DEBUG is on).
- **Modify** `main.go` — read `ESSIE3_DEBUG`, conditionally wrap the handler with `WithDebugLogging`, add a `debug: enabled` line to the startup banner.
- **Modify** `README.md` — document `ESSIE3_DEBUG` in the env-var table.

---

### Task 1: `debugResponseWriter` — capture status and bytes

A small `http.ResponseWriter` wrapper that records the status code (defaulting to 200 if `WriteHeader` is never called) and the number of bytes written. Pure plumbing — no formatting logic yet.

**Files:**
- Create: `debug.go`
- Create: `debug_test.go`

- [ ] **Step 1: Write the failing test**

Create `debug_test.go` with initial contents:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestDebugResponseWriter -v`
Expected: compile failure — `undefined: newDebugResponseWriter`.

- [ ] **Step 3: Create `debug.go` with the wrapper**

```go
package main

import (
	"net/http"
)

// debugResponseWriter wraps an http.ResponseWriter so the debug
// middleware can record the final status code and total bytes written
// without changing what the client sees.
type debugResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newDebugResponseWriter(w http.ResponseWriter) *debugResponseWriter {
	return &debugResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (d *debugResponseWriter) WriteHeader(code int) {
	if d.wroteHeader {
		return
	}
	d.wroteHeader = true
	d.status = code
	d.ResponseWriter.WriteHeader(code)
}

func (d *debugResponseWriter) Write(p []byte) (int, error) {
	if !d.wroteHeader {
		// Match net/http's implicit-200 behavior so a Write without a
		// prior WriteHeader still has a meaningful captured status.
		d.wroteHeader = true
	}
	n, err := d.ResponseWriter.Write(p)
	d.bytes += n
	return n, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestDebugResponseWriter -v`
Expected: PASS for all three subtests.

- [ ] **Step 5: Commit**

```bash
git add debug.go debug_test.go
git commit -m "Add debugResponseWriter to capture status and bytes written"
```

---

### Task 2: `formatRequest` — render the request block

A pure function that builds the `--> METHOD PATH` line plus sorted, indented header lines. Multi-value headers expand to one line per value. No I/O.

**Files:**
- Modify: `debug.go`
- Modify: `debug_test.go`

- [ ] **Step 1: Write the failing test**

Append to `debug_test.go`:

```go
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
```

You will also need to add `"strings"` to the test file's import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestFormatRequest -v`
Expected: compile failure — `undefined: formatRequest`.

- [ ] **Step 3: Implement `formatRequest`**

Append to `debug.go`:

```go
import (
	"net/http"
	"sort"
	"strings"
)
```

(replace the existing import block — the package already has `"net/http"`; you are adding `"sort"` and `"strings"`).

Then add the function:

```go
// formatRequest renders the multi-line request block: the `--> METHOD
// PATH` line followed by one indented line per header value, sorted by
// header name for stable, diff-friendly output.
func formatRequest(r *http.Request) string {
	var b strings.Builder
	b.WriteString("--> ")
	b.WriteString(r.Method)
	b.WriteByte(' ')
	b.WriteString(r.URL.RequestURI())
	b.WriteByte('\n')
	writeHeaders(&b, r.Header)
	return b.String()
}

// writeHeaders writes header values to b in alphabetical order by name,
// one indented line per value (multi-value headers get one line each).
func writeHeaders(b *strings.Builder, h http.Header) {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range h[name] {
			b.WriteString("    ")
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteByte('\n')
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestFormatRequest -v`
Expected: PASS for all four subtests.

- [ ] **Step 5: Commit**

```bash
git add debug.go debug_test.go
git commit -m "Add formatRequest for DEBUG request block"
```

---

### Task 3: `formatResponse` — render the response block

A pure function that builds `<-- CODE STATUS_TEXT (elapsed, N bytes)` plus sorted, indented response-header lines. Reuses `writeHeaders` from Task 2.

**Files:**
- Modify: `debug.go`
- Modify: `debug_test.go`

- [ ] **Step 1: Write the failing test**

Append to `debug_test.go`:

```go
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
```

You will need to add `"time"` to the test file's import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestFormatResponse -v`
Expected: compile failure — `undefined: formatResponse`.

- [ ] **Step 3: Implement `formatResponse`**

Add `"fmt"` and `"time"` to `debug.go`'s import block.

Then append to `debug.go`:

```go
// formatResponse renders the multi-line response block: the `<-- CODE
// STATUS (elapsed, N bytes)` line followed by sorted response headers.
func formatResponse(d *debugResponseWriter, elapsed time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<-- %d %s (%s, %d bytes)\n",
		d.status, http.StatusText(d.status), elapsed, d.bytes)
	writeHeaders(&b, d.Header())
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestFormatResponse -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Commit**

```bash
git add debug.go debug_test.go
git commit -m "Add formatResponse for DEBUG response block"
```

---

### Task 4: `WithDebugLogging` middleware

The HTTP middleware that ties it all together: print the request block, delegate to the wrapped handler (using `debugResponseWriter`), then print the response block with elapsed time. Output goes through a per-middleware `*log.Logger` so concurrent requests cannot interleave their blocks. The `io.Writer` is injected for testability.

**Files:**
- Modify: `debug.go`
- Modify: `debug_test.go`

- [ ] **Step 1: Write the failing test**

Append to `debug_test.go`:

```go
func TestWithDebugLogging_PrintsRequestAndResponse(t *testing.T) {
	var buf bytes.Buffer
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"xyz"`)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("ok"))
	})
	mw := WithDebugLogging(inner, &buf)

	req := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader("body"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	// Inner handler's response must reach the client unchanged.
	if rec.Code != http.StatusCreated {
		t.Errorf("client-visible status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("client-visible body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Get("Etag"); got != `"xyz"` {
		t.Errorf("client-visible Etag = %q, want %q", got, `"xyz"`)
	}

	out := buf.String()
	if !strings.Contains(out, "--> PUT /bucket/key\n") {
		t.Errorf("missing request line in debug output:\n%s", out)
	}
	if !strings.Contains(out, "    Content-Type: text/plain\n") {
		t.Errorf("missing request header in debug output:\n%s", out)
	}
	if !strings.Contains(out, "<-- 201 Created (") {
		t.Errorf("missing response line in debug output:\n%s", out)
	}
	if !strings.Contains(out, "    Etag: \"xyz\"\n") {
		t.Errorf("missing response header in debug output:\n%s", out)
	}

	// Request block must come before response block.
	if strings.Index(out, "--> PUT") > strings.Index(out, "<-- 201") {
		t.Errorf("request block printed after response block:\n%s", out)
	}
}

func TestWithDebugLogging_ImplicitStatusIs200(t *testing.T) {
	var buf bytes.Buffer
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi")) // no WriteHeader — implicit 200
	})
	mw := WithDebugLogging(inner, &buf)

	req := httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !strings.Contains(buf.String(), "<-- 200 OK (") {
		t.Errorf("expected implicit-200 status in output:\n%s", buf.String())
	}
}
```

You will need to add `"bytes"` to the test file's import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestWithDebugLogging -v`
Expected: compile failure — `undefined: WithDebugLogging`.

- [ ] **Step 3: Implement `WithDebugLogging`**

Add `"io"` and `"log"` to `debug.go`'s import block.

Then append to `debug.go`:

```go
// WithDebugLogging returns an http.Handler that prints a multi-line
// request block before delegating to next, then a response block with
// the captured status, byte count, and elapsed time. Output is written
// to out (typically os.Stderr). A per-middleware *log.Logger serializes
// writes so concurrent requests do not interleave their blocks.
func WithDebugLogging(next http.Handler, out io.Writer) http.Handler {
	logger := log.New(out, "", 0)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger.Print(formatRequest(r))
		drw := newDebugResponseWriter(w)
		next.ServeHTTP(drw, r)
		logger.Print(formatResponse(drw, time.Since(start)))
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestWithDebugLogging -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Run the full test suite to make sure nothing else broke**

Run: `go test ./...`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add debug.go debug_test.go
git commit -m "Add WithDebugLogging HTTP middleware"
```

---

### Task 5: Wire DEBUG into `main.go` and remove the duplicate log line

Read `ESSIE3_DEBUG` in `main.go`, wrap the handler with `WithDebugLogging` when enabled, and extend the startup banner. Then remove the existing per-request `log.Printf("%s %s", ...)` line from `handler.go:25` so requests aren't double-logged when DEBUG is on.

**Files:**
- Modify: `main.go`
- Modify: `handler.go`

- [ ] **Step 1: Wire DEBUG into `main.go`**

In `main.go`, after the existing `auth := AuthConfig{...}` block and before the `fmt.Printf("essie3 starting on :%s\n", port)` line, add:

```go
debug := os.Getenv("ESSIE3_DEBUG") == "true"
```

Then, in the startup-banner block, after the auth lines (`if auth.Enabled() { ... } else { ... }`), add:

```go
if debug {
    fmt.Printf("  debug:    enabled\n")
}
```

Then change the handler construction from:

```go
srv := &http.Server{
    Addr:              ":" + port,
    Handler:           NewHandler(storage, fallback, auth),
    ...
}
```

to:

```go
var handler http.Handler = NewHandler(storage, fallback, auth)
if debug {
    handler = WithDebugLogging(handler, os.Stderr)
}
srv := &http.Server{
    Addr:              ":" + port,
    Handler:           handler,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       5 * time.Minute,
    WriteTimeout:      5 * time.Minute,
    IdleTimeout:       2 * time.Minute,
}
```

(Keep the existing timeout values.) Also add `"net/http"` to the imports if `go vet` complains — it's already there because `http.ErrServerClosed` is used below.

- [ ] **Step 2: Remove the duplicate log line in `handler.go`**

In `handler.go`, in the `ServeHTTP` method, delete this line (around line 25):

```go
log.Printf("%s %s", r.Method, r.URL.Path)
```

This was the only use of `log` in `handler.go`. Go imports are per-file, so you must also remove `"log"` from `handler.go`'s import block — otherwise the build will fail with `"log" imported and not used`. (`auth.go` has its own separate `"log"` import, which stays.)

- [ ] **Step 3: Verify the build**

Run: `go build ./...`
Expected: success, no errors.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: all tests pass. The existing tests do not set `ESSIE3_DEBUG`, so behavior is unchanged for them.

- [ ] **Step 5: Smoke-test DEBUG=on manually**

Run, in one terminal:

```bash
ESSIE3_DEBUG=true PORT=9999 DATA_DIR=/tmp/essie3-debug-smoke go run .
```

Then in another terminal:

```bash
curl -X PUT http://localhost:9999/mybucket
curl -X PUT --data-binary "hello" -H "Content-Type: text/plain" http://localhost:9999/mybucket/key.txt
curl http://localhost:9999/mybucket/key.txt
```

Expected on the server's stderr: for each request, a `--> METHOD PATH` block with indented headers, followed by a `<-- CODE STATUS (elapsed, N bytes)` block. Stop the server with Ctrl-C and `rm -rf /tmp/essie3-debug-smoke`.

- [ ] **Step 6: Smoke-test DEBUG=off**

Run, in one terminal:

```bash
PORT=9999 DATA_DIR=/tmp/essie3-debug-smoke go run .
```

In another terminal:

```bash
curl -X PUT http://localhost:9999/mybucket
```

Expected: the server prints its startup banner (no `debug:` line) and **nothing** per-request — the previous `Method Path` line is gone. Stop with Ctrl-C and `rm -rf /tmp/essie3-debug-smoke`.

- [ ] **Step 7: Commit**

```bash
git add main.go handler.go
git commit -m "Wire ESSIE3_DEBUG env var and remove duplicate per-request log line"
```

---

### Task 6: Document `ESSIE3_DEBUG` in the README

Add a row to the env-var configuration table.

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add the row**

In `README.md`, find the configuration table that begins with `| Variable ... | Default ... | Description ... |` (around line 65). After the `ESSIE3_FALLBACK_PUBLIC` row, add:

```
| `ESSIE3_DEBUG`               | *(unset)*         | When set to `true`, log full request and response details (method, path, headers, status, timing) to stderr. Useful when debugging integration tests, especially auth-failure paths. Off by default. |
```

- [ ] **Step 2: Verify the markdown still renders cleanly**

Run: `grep -c '|' README.md` (sanity check — table rows should have a consistent number of pipes).

Visually scan the table to make sure column alignment is preserved.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "Document ESSIE3_DEBUG env var"
```

---

## Self-Review Notes

**Spec coverage:**
- Env var `ESSIE3_DEBUG=true` → Task 5.
- Middleware architecture / `debug.go` location → Tasks 1–4.
- `debugResponseWriter` with default-200 and byte counting → Task 1.
- `formatRequest` / `formatResponse` pure functions → Tasks 2, 3.
- Multi-line human-readable format, 4-space indent → Tasks 2, 3.
- Sorted headers, multi-value support → Task 2 (verified in tests).
- `Authorization` shown in full, no redaction → Task 2 test uses an `Authorization` header verbatim.
- No body logging → no task touches request/response bodies in the formatters.
- stderr output via mutex-protected `*log.Logger` → Task 4.
- Injected `io.Writer` for testability → Task 4 signature.
- Startup-banner extension (`debug: enabled`) → Task 5.
- Removal of duplicate `log.Printf` in `handler.go:25` → Task 5.
- README documentation → Task 6.
- Tests covering writer wrapper, formatters, and middleware end-to-end → Tasks 1–4.

**Out of scope (confirmed not in plan):** body logging, header redaction, JSON output, `log/slog` migration, per-route filtering.
