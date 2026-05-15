# `handleCopyObject` Error Mapping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `handleCopyObject`'s blanket `404 NoSuchKey` error response with `errors.Is`-based branching that returns `400 InvalidArgument` for `errInvalidName`, `404 NoSuchKey` for `os.ErrNotExist`, and `500 InternalError` for anything else. Closes audit critical finding #2.

**Architecture:** Single change site at the error branch in `handler.go`'s `handleCopyObject`. `errors.Is` walks any `%w`-wrapped chain so the existing `Storage` returns work unchanged. Three integration tests cover the three response shapes; the existing happy-path test (`TestHandler_CopyObject`) stays as-is.

**Tech Stack:** Go 1.22, standard library only (`errors`, `os`).

**Spec:** [`docs/superpowers/specs/2026-05-15-copy-object-errors-design.md`](../specs/2026-05-15-copy-object-errors-design.md)

---

## File Structure

- **Modify** `handler.go` — replace the error branch in `handleCopyObject` (around line 311); add `"os"` to the import block (`errors` is already imported).
- **Modify** `handler_test.go` — append two new integration tests for the 400 and 404 failure paths.

---

### Task 1: Replace blanket 404 with `errors.Is` branching

Single self-contained change. TDD ordering: write the two new failure-path tests, watch them fail (the 400 test sees a 404 — the bug being fixed), implement, watch them pass.

**Files:**
- Modify: `handler.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `handler_test.go`:

```go
func TestHandler_CopyObject_InvalidSourceReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Seed a real source to make the request well-formed apart from
	// the malicious copy-source header.
	put, _ := http.NewRequest("PUT", srv.URL+"/b/src.txt", strings.NewReader("hello"))
	http.DefaultClient.Do(put)

	req, _ := http.NewRequest("PUT", srv.URL+"/b/dst.txt", nil)
	req.Header.Set("x-amz-copy-source", "/b/../escape")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_CopyObject_MissingSourceReturns404(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/dst.txt", nil)
	req.Header.Set("x-amz-copy-source", "/b/missing-key.txt")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>NoSuchKey</Code>") {
		t.Errorf("body missing <Code>NoSuchKey</Code>:\n%s", body)
	}
	if !strings.Contains(string(body), "<Key>missing-key.txt</Key>") {
		t.Errorf("body missing <Key>missing-key.txt</Key>:\n%s", body)
	}
}
```

(All needed imports — `io`, `net/http`, `strings`, `testing` — are already in `handler_test.go`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_CopyObject_(InvalidSource|MissingSource)" -v`
Expected: `TestHandler_CopyObject_InvalidSourceReturns400` FAILS with `status = 404, want 400` (the bug we're fixing — invalid name currently returns 404). `TestHandler_CopyObject_MissingSourceReturns404` PASSES (the existing blanket-404 path coincidentally produces the right status for this case, but missing the precise error code/key in the body — so it may fail on the `<Key>missing-key.txt</Key>` assertion if the current code doesn't include that). If both checks against the body pass coincidentally, that's fine — the implementation is still correct.

- [ ] **Step 3: Replace the error branch in `handleCopyObject`**

In `handler.go`, add `"os"` to the import block. The full updated import block:

```go
import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)
```

Then replace the body of `handleCopyObject`. The current code is:

```go
func (h *Handler) handleCopyObject(w http.ResponseWriter, dstBucket, dstKey, copySource string) {
	// copySource format: /<bucket>/<key>
	source := strings.TrimPrefix(copySource, "/")
	parts := strings.SplitN(source, "/", 2)
	if len(parts) != 2 {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", "Invalid x-amz-copy-source", dstBucket, dstKey)
		return
	}
	srcBucket, srcKey := parts[0], parts[1]

	etag, err := h.storage.CopyObject(srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		writeNoSuchKey(w, srcBucket, srcKey)
		return
	}

	writeCopyObjectResult(w, etag)
}
```

Replace with:

```go
func (h *Handler) handleCopyObject(w http.ResponseWriter, dstBucket, dstKey, copySource string) {
	// copySource format: /<bucket>/<key>
	source := strings.TrimPrefix(copySource, "/")
	parts := strings.SplitN(source, "/", 2)
	if len(parts) != 2 {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", "Invalid x-amz-copy-source", dstBucket, dstKey)
		return
	}
	srcBucket, srcKey := parts[0], parts[1]

	etag, err := h.storage.CopyObject(srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidName):
			writeXMLError(w, http.StatusBadRequest, "InvalidArgument", err.Error(), dstBucket, dstKey)
		case errors.Is(err, os.ErrNotExist):
			writeNoSuchKey(w, srcBucket, srcKey)
		default:
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), dstBucket, dstKey)
		}
		return
	}

	writeCopyObjectResult(w, etag)
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_CopyObject_(InvalidSource|MissingSource)" -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Run the full suite to confirm no regression**

Run: `cd /Users/igor/workspace/essie3 && go test -race -count=1 ./...`
Expected: all tests pass under `-race` (CI's invocation). The pre-existing `TestHandler_CopyObject` happy-path test is unaffected — the success branch of the function is unchanged.

- [ ] **Step 6: Commit**

```bash
git add handler.go handler_test.go
git commit -m "Map handleCopyObject errors to InvalidArgument/NoSuchKey/InternalError"
```

---

## Self-Review Notes

**Spec coverage:**
- `errors.Is(err, errInvalidName)` → 400 `InvalidArgument` → Task 1.
- `errors.Is(err, os.ErrNotExist)` → 404 `NoSuchKey` with src identifiers → Task 1.
- Default → 500 `InternalError` → Task 1.
- `"os"` import added → Task 1 (Step 3).
- Three integration tests — invalid source (400), missing source (404), and the existing happy-path test left as-is → Task 1 (Steps 1, 5).

**Out of scope (confirmed not in plan):** wrapping errors in `Storage.CopyObject` to distinguish src vs dst, source-ACL check on copy, README/CLAUDE.md updates (no user-facing copy-error behavior is documented today).
