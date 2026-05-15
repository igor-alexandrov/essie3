# GET/HEAD Invalid-Name Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `handleGetObject` and `handleHeadObject` return `400 InvalidArgument` when the bucket or key fails `validateName`, instead of falling through to the fallback placeholder path. Closes audit critical finding #3.

**Architecture:** A two-line `errors.Is(err, errInvalidName)` early return at the top of each handler, right after the storage call. Auth and fallback logic are unchanged. No new imports.

**Tech Stack:** Go 1.22, standard library only (`errors`).

**Spec:** [`docs/superpowers/specs/2026-05-15-invalid-name-fallback-design.md`](../specs/2026-05-15-invalid-name-fallback-design.md)

---

## File Structure

- **Modify** `handler.go` — two two-line insertions, one in `handleGetObject` and one in `handleHeadObject`. No new imports (`errors` is already imported).
- **Modify** `handler_test.go` — append two new integration tests covering the GET and HEAD invalid-name paths.

---

### Task 1: Reject invalid names in GET and HEAD before fallback

Single self-contained change: write the two failing integration tests, watch them fail (current code returns 200 with a fallback placeholder for `GET /../escape/photo.jpg`), insert the `errors.Is` guard in both handlers, watch them pass.

**Files:**
- Modify: `handler.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `handler_test.go`:

```go
func TestHandler_GetObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Bucket "../escape" is invalid per validateName. Without the fix,
	// the handler falls through to fallback.Select on the .jpg key and
	// serves a placeholder with 200 OK.
	resp, err := http.Get(srv.URL + "/../escape/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml (must not serve fallback bytes)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_HeadObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/../escape/photo.jpg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
```

(All needed imports — `io`, `net/http`, `strings`, `testing` — are already in `handler_test.go`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_(Get|Head)Object_InvalidNameReturns400" -v`
Expected: both subtests FAIL. The GET subtest fails with `status = 200, want 400` (the bug — fallback returns a placeholder with 200 OK); the HEAD subtest fails the same way.

- [ ] **Step 3: Add the `errors.Is` guard in `handleGetObject`**

In `handler.go`, in `handleGetObject`, change:

```go
func (h *Handler) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, objErr := h.storage.GetObject(bucket, key)

	var acl string
	if objErr == nil {
		acl = obj.Meta.ACL
	}
```

to:

```go
func (h *Handler) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, objErr := h.storage.GetObject(bucket, key)
	if errors.Is(objErr, errInvalidName) {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", objErr.Error(), bucket, key)
		return
	}

	var acl string
	if objErr == nil {
		acl = obj.Meta.ACL
	}
```

- [ ] **Step 4: Add the `errors.Is` guard in `handleHeadObject`**

In `handler.go`, in `handleHeadObject`, change:

```go
func (h *Handler) handleHeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, metaErr := h.storage.HeadObject(bucket, key)

	var acl string
	if metaErr == nil {
		acl = meta.ACL
	}
```

to:

```go
func (h *Handler) handleHeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, metaErr := h.storage.HeadObject(bucket, key)
	if errors.Is(metaErr, errInvalidName) {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", metaErr.Error(), bucket, key)
		return
	}

	var acl string
	if metaErr == nil {
		acl = meta.ACL
	}
```

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestHandler_(Get|Head)Object_InvalidNameReturns400" -v`
Expected: PASS for both subtests.

- [ ] **Step 6: Run the full suite to confirm no regression**

Run: `cd /Users/igor/workspace/essie3 && go test -race -count=1 ./...`
Expected: all tests pass under `-race`. The pre-existing fallback tests (`TestHandler_GetObject_FallbackImage`, `TestHandler_GetObject_FallbackForAnyMissingKey`, `TestHandler_HeadObject_FallbackImage`) all use well-formed keys and continue to pass — the new branch only fires on `errInvalidName`.

- [ ] **Step 7: Commit**

```bash
git add handler.go handler_test.go
git commit -m "Reject invalid names in GET/HEAD before consulting fallback"
```

---

## Self-Review Notes

**Spec coverage:**
- `handleGetObject` early `errors.Is` return → Task 1 (Step 3).
- `handleHeadObject` early `errors.Is` return → Task 1 (Step 4).
- 400 beats auth (the new branch fires before `h.auth.authorize`) → Tasks 1 (Steps 3 and 4 both insert before the existing acl/auth lines).
- 400 beats fallback (new branch fires before the fallback consultation) → same.
- Test asserting GET returns 400 with `application/xml` body and `<Code>InvalidArgument</Code>` (and crucially NOT a fallback placeholder) → Task 1 (Step 1).
- Test asserting HEAD returns 400 → Task 1 (Step 1).
- Existing fallback tests unaffected → Task 1 (Step 6).

**Out of scope (confirmed not in plan):** bucket-level pre-validation in `handleObject`, `handleCopyObject` (PR #8), `DeleteObject` invalid-name asymmetry (audit finding #4), Storage layer changes.
