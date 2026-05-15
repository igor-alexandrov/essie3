# DELETE Invalid-Name Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `DELETE /b/<bad-name>` return `400 InvalidArgument` instead of silently returning `204 No Content`. Closes audit critical finding #4 (DELETE asymmetry) and minor finding #12 (`Storage.DeleteObject` swallows errors).

**Architecture:** Change `Storage.DeleteObject`'s signature from `()` to `(error)`. It returns `errInvalidName` when validation fails, `nil` otherwise (preserving the idempotent semantics for missing-key and IO-error cases). The handler captures the new return and translates `errInvalidName` to 400 via `errors.Is`. Two unit tests assert the Storage contract directly; two integration tests cover the handler path.

**Tech Stack:** Go 1.22, standard library only (`errors`).

**Spec:** [`docs/superpowers/specs/2026-05-15-delete-invalid-name-design.md`](../specs/2026-05-15-delete-invalid-name-design.md)

---

## File Structure

- **Modify** `storage.go` — change `DeleteObject` signature to return `error`; return `errInvalidName` from the validation branches; return `nil` after the IO calls.
- **Modify** `handler.go` — capture the new return in the `MethodDelete` branch of `handleObject`; translate `errInvalidName` → 400.
- **Modify** `storage_test.go` — append two new unit tests asserting the Storage contract.
- **Modify** `handler_test.go` — append two new integration tests covering the handler paths.

---

### Task 1: Make `DeleteObject` return `error` and propagate to the handler

Single self-contained change. TDD ordering: write the failing tests, watch them fail (the handler currently returns 204 for invalid names; Storage's old void signature compiles fine but the Storage tests fail because `DeleteObject` doesn't return anything to assert on), update Storage's signature, update the handler, watch them pass.

**Files:**
- Modify: `storage.go`
- Modify: `handler.go`
- Modify: `storage_test.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing Storage unit tests**

Append to `storage_test.go`:

```go
func TestStorage_DeleteObject_InvalidNameReturnsError(t *testing.T) {
	s := NewStorage(t.TempDir())

	if err := s.DeleteObject("..", "k"); !errors.Is(err, errInvalidName) {
		t.Errorf("DeleteObject(..) error = %v, want errInvalidName", err)
	}
	if err := s.DeleteObject("b", "../escape"); !errors.Is(err, errInvalidName) {
		t.Errorf("DeleteObject(b, ../escape) error = %v, want errInvalidName", err)
	}
}

func TestStorage_DeleteObject_MissingFilesReturnsNil(t *testing.T) {
	s := NewStorage(t.TempDir())

	if err := s.DeleteObject("b", "never-existed.txt"); err != nil {
		t.Errorf("DeleteObject on never-existed key = %v, want nil (idempotent)", err)
	}
}
```

Add `"errors"` to `storage_test.go`'s import block:

```go
import (
	"errors"
	"os"
	"testing"
)
```

- [ ] **Step 2: Write the failing handler integration tests**

Append to `handler_test.go`:

```go
func TestHandler_DeleteObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/b/../escape", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (must not silently 204)", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_DeleteObject_MissingKeyReturns204(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create the bucket but never put the key.
	put, _ := http.NewRequest("PUT", srv.URL+"/b", nil)
	http.DefaultClient.Do(put)

	req, _ := http.NewRequest("DELETE", srv.URL+"/b/never-existed.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (idempotent DELETE)", resp.StatusCode)
	}
}
```

(All needed imports — `io`, `net/http`, `strings`, `testing` — are already in `handler_test.go`.)

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestStorage_DeleteObject_(InvalidName|MissingFiles)|TestHandler_DeleteObject_(InvalidNameReturns400|MissingKeyReturns204)" -v`
Expected:
- The two `TestStorage_DeleteObject_*` tests fail to **compile** with `s.DeleteObject(...) used as value`. This is the right kind of failure — Storage's signature must change.
- The two new `TestHandler_DeleteObject_*` tests will not run because of the compile failure. After we fix Storage, the `InvalidNameReturns400` test will fail with `status = 204, want 400` (the bug we're fixing); `MissingKeyReturns204` will pass coincidentally on the unfixed handler.

- [ ] **Step 4: Change `Storage.DeleteObject` to return `error`**

In `storage.go`, replace `DeleteObject`:

```go
func (s *Storage) DeleteObject(bucket, key string) error {
	if err := validateName(bucket); err != nil {
		return err
	}
	if err := validateName(key); err != nil {
		return err
	}
	mu := s.keyMutex(bucket, key)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(s.objectPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete object %s/%s: %v", bucket, key, err)
	}
	if err := os.Remove(s.metaPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete meta %s/%s: %v", bucket, key, err)
	}
	return nil
}
```

If the version of `storage.go` on this branch does NOT yet have the `keyMutex` lock lines (it shouldn't on a fresh branch from main today), drop those four lines:

```go
func (s *Storage) DeleteObject(bucket, key string) error {
	if err := validateName(bucket); err != nil {
		return err
	}
	if err := validateName(key); err != nil {
		return err
	}
	if err := os.Remove(s.objectPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete object %s/%s: %v", bucket, key, err)
	}
	if err := os.Remove(s.metaPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete meta %s/%s: %v", bucket, key, err)
	}
	return nil
}
```

(`validateName` returns `errInvalidName` itself on bad inputs, so propagating its return preserves the sentinel for the handler's `errors.Is` check.)

- [ ] **Step 5: Update the handler's DELETE branch**

In `handler.go`, in `handleObject`, replace the `case http.MethodDelete:` block. The current code is:

```go
case http.MethodDelete:
    if e := h.auth.authorize(r, opWrite, ""); e != nil {
        writeAuthError(w, e, bucket, key)
        return
    }
    h.storage.DeleteObject(bucket, key)
    w.WriteHeader(http.StatusNoContent)
```

Replace with:

```go
case http.MethodDelete:
    if e := h.auth.authorize(r, opWrite, ""); e != nil {
        writeAuthError(w, e, bucket, key)
        return
    }
    if err := h.storage.DeleteObject(bucket, key); err != nil {
        if errors.Is(err, errInvalidName) {
            writeXMLError(w, http.StatusBadRequest, "InvalidArgument", err.Error(), bucket, key)
            return
        }
        writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket, key)
        return
    }
    w.WriteHeader(http.StatusNoContent)
```

(The `errors` import is already in `handler.go` — used by other handlers' `errors.Is` calls.)

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestStorage_DeleteObject_(InvalidName|MissingFiles)|TestHandler_DeleteObject_(InvalidNameReturns400|MissingKeyReturns204)" -v`
Expected: PASS for all four subtests.

- [ ] **Step 7: Run the full suite to confirm no regression**

Run: `cd /Users/igor/workspace/essie3 && go test -race -count=1 ./...`
Expected: all tests pass under `-race`. The pre-existing `TestHandler_DeleteObject` (PUT then DELETE then GET 404) is unaffected — DeleteObject's new return is `nil` on success, so the handler still writes 204.

- [ ] **Step 8: Commit**

```bash
git add storage.go storage_test.go handler.go handler_test.go
git commit -m "Return error from Storage.DeleteObject and translate errInvalidName to 400"
```

---

## Self-Review Notes

**Spec coverage:**
- `Storage.DeleteObject` gains an `error` return → Task 1 (Step 4).
- Returns `errInvalidName` from validation, `nil` otherwise → Task 1 (Step 4).
- Handler translates `errInvalidName` to 400, default 500 for safety → Task 1 (Step 5).
- `log.Printf` for IO errors preserved (idempotent DELETE) → Task 1 (Step 4).
- Unit tests for the Storage contract → Task 1 (Step 1).
- Integration test for the 400 response → Task 1 (Step 2).
- Integration test asserting the missing-key idempotent path still returns 204 → Task 1 (Step 2).
- Existing `TestHandler_DeleteObject` happy-path test unchanged → Task 1 (Step 7).

**Out of scope (confirmed not in plan):** bucket-level DELETE (not implemented), translating IO errors to 500 (deliberately preserved as 204 for idempotency), other audit findings.
