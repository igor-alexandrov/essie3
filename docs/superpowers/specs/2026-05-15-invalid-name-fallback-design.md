# GET/HEAD invalid-name fallback — design

## Goal

Close audit critical finding #3: `handleGetObject` and `handleHeadObject` currently treat **every** `Storage.GetObject`/`HeadObject` error as "object missing" and consult the fallback. A request like `GET /../escape/photo.jpg` makes Storage fail `validateName`, then `Fallback.Select` matches on `.jpg` and returns a placeholder with `200 OK`. No filesystem reach (so not a path-traversal vuln), but the wrong status is served — should be `400 InvalidArgument`.

After this change, the two handlers branch on `errors.Is(err, errInvalidName)` immediately after the storage call and return `400 InvalidArgument` before either auth or fallback logic runs. All other error cases (genuine missing file, IO error, parse error) fall through to the existing path unchanged.

## Architecture

Two single-line additions, one per handler. Both go right after the `obj, objErr := h.storage.GetObject(...)` (or `meta, metaErr := h.storage.HeadObject(...)`) line and before the existing `var acl string` block.

For `handleGetObject`:

```go
obj, objErr := h.storage.GetObject(bucket, key)
if errors.Is(objErr, errInvalidName) {
    writeXMLError(w, http.StatusBadRequest, "InvalidArgument", objErr.Error(), bucket, key)
    return
}
// ... existing acl/auth/fallback logic unchanged
```

For `handleHeadObject`:

```go
meta, metaErr := h.storage.HeadObject(bucket, key)
if errors.Is(metaErr, errInvalidName) {
    writeXMLError(w, http.StatusBadRequest, "InvalidArgument", metaErr.Error(), bucket, key)
    return
}
// ... existing acl/auth/fallback logic unchanged
```

`errors.Is` is already in scope (`handler.go` imports `"errors"`). `errInvalidName` is the existing sentinel exported from `storage.go` (package-private; same package `main`).

## Why this branch beats auth and beats fallback

The `errInvalidName` check runs before `h.auth.authorize`. If auth is enabled and an unauthenticated client sends `GET /../foo/photo.jpg`, the response is 400 (invalid name), not 403 (auth fail). For a dev/test stand-in this is the more useful signal — a malformed-name failure shouldn't be masked by an auth response. The audit's recommendation aligns with this ordering.

The `errInvalidName` check runs before the fallback consultation. The fallback only ever runs when the requested key is well-formed but the object is genuinely missing — exactly the documented "deterministic placeholder for missing object" use case.

## What stays unchanged

- Genuine missing-object path (`os.ErrNotExist`-equivalent from `os.ReadFile`) → fallback consulted; if no matching extension, `404 NoSuchKey`.
- Auth-failure path on a valid name with a missing object → `writeAuthError` (or fallback if `FallbackPublic`).
- Happy path (object exists) → unchanged.
- `handleCopyObject` is unaffected — PR #8 already maps `errInvalidName` to 400 there.
- Storage layer is unaffected.
- `handleObject`'s top-level dispatch is unaffected — PUT/DELETE on invalid names still go through Storage's existing validation (see audit finding #4 for the DELETE-specific case).

## Testing

Two new integration tests in `handler_test.go`:

- `TestHandler_GetObject_InvalidNameReturns400` — `GET /../escape/photo.jpg`. Assert status 400 and body contains `<Code>InvalidArgument</Code>`. Critically, also assert the response body is **not** the bytes of any fallback placeholder (e.g. by checking the `Content-Type` is `application/xml`, not `image/*`).
- `TestHandler_HeadObject_InvalidNameReturns400` — same path with HEAD. Assert status 400.

Existing tests that exercise the genuine-missing-object fallback path (`TestHandler_GetObject_FallbackImage`, `TestHandler_GetObject_FallbackForAnyMissingKey`, `TestHandler_HeadObject_FallbackImage`) all use well-formed keys and continue to pass — the new branch only fires on `errInvalidName`.

## File changes summary

- **Modify** `handler.go` — two two-line insertions in `handleGetObject` and `handleHeadObject`. No new imports.
- **Modify** `handler_test.go` — append two new tests.

## Out of scope

- Bucket-level validation in `handleObject` itself (the dispatch layer): bucket validation already happens inside Storage on every method call; the audit didn't flag a separate gap there.
- The `handleCopyObject` error mapping (PR #8).
- The `DeleteObject` invalid-name asymmetry (audit finding #4 — separate fix).
- Changing the order of auth vs. invalid-name checks for any handler other than `handleGetObject` / `handleHeadObject`.
