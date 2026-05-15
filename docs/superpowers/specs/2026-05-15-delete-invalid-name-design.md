# DELETE invalid-name handling — design

## Goal

Close audit critical finding #4: `DELETE /b/../escape` currently
returns `204 No Content` because `Storage.DeleteObject` validates the
name internally and swallows every failure. PUT on the same name
returns `400 InvalidArgument`. The asymmetry misleads integration
tests and contradicts the principle that malformed requests should
fail loudly.

The same change closes audit minor finding #12 ("`Storage.DeleteObject`
swallows errors via `log.Printf`"): the storage interface becomes
consistent — every method returns `error` — and the handler decides
how to react.

## Architecture

`Storage.DeleteObject` gains an `error` return:

```go
// DeleteObject removes the body and meta files for bucket/key.
// Returns errInvalidName if either name fails validation. Real IO
// errors during the os.Remove calls are logged but not propagated:
// DELETE is idempotent — a missing file or a stat-failure should not
// fail the request. Returns nil on every other path.
func (s *Storage) DeleteObject(bucket, key string) error
```

Behavior matrix:

| Input                              | Return         | Side effect                                |
| ---------------------------------- | -------------- | ------------------------------------------ |
| Valid name, files exist            | `nil`          | Both files removed                         |
| Valid name, files missing          | `nil`          | No-op (idempotent)                         |
| Valid name, IO error on `os.Remove`| `nil`          | `log.Printf` records the failure           |
| `errInvalidName` for bucket or key | `errInvalidName` | No filesystem touch                       |

The handler translates the new return:

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

The `default` 500 branch is dead code today (only `errInvalidName` is
ever returned) but it preserves safety if a future change starts
propagating other errors.

## What stays unchanged

- DELETE on a missing-but-valid key still returns `204 No Content`.
  The idempotent semantic is the documented S3 contract.
- Auth check ordering (auth before delete) is preserved.
- The internal `log.Printf` calls inside `DeleteObject` for real IO
  errors stay — they remain the only operator-visible signal of a
  filesystem-level failure.
- Other Storage callers of `DeleteObject` — none today — would need
  to handle the new return. There are no other callers in the
  codebase.

## Testing

Three integration tests in `handler_test.go`:

- `TestHandler_DeleteObject_InvalidNameReturns400` —
  `DELETE /b/../escape` returns 400 with `Content-Type: application/xml`
  and body containing `<Code>InvalidArgument</Code>`. Asserts the
  response is **not** silent 204.
- `TestHandler_DeleteObject_MissingKeyReturns204` — explicit regression
  guard for the idempotent path: `DELETE /b/never-existed.txt` (with a
  freshly-created bucket and no prior PUT for that key) returns 204.
- The pre-existing `TestHandler_DeleteObject` (PUT then DELETE then
  GET 404) is unchanged.

A unit test in `storage_test.go` (or a new file) covers the Storage
contract directly:

- `TestStorage_DeleteObject_InvalidNameReturnsError` — calling
  `DeleteObject("..", "k")` returns `errInvalidName` (asserted via
  `errors.Is`).
- `TestStorage_DeleteObject_MissingFilesReturnsNil` — calling
  `DeleteObject("b", "missing")` on a fresh data dir returns `nil`.

## File changes summary

- **Modify** `storage.go` — change `DeleteObject` return type from
  `()` to `(error)`, return `errInvalidName` from the validation
  branches, return `nil` after the two `os.Remove` calls.
- **Modify** `handler.go` — capture the new return from `DeleteObject`,
  branch on `errors.Is(err, errInvalidName)`.
- **Modify** `storage_test.go` — append two new unit tests.
- **Modify** `handler_test.go` — append two new integration tests.

## Out of scope

- Bucket-level `DELETE` (not implemented; falls through to
  `MethodNotAllowed`).
- Translating IO errors to a 500 (the spec deliberately preserves the
  log-and-swallow behavior for IO errors to maintain idempotent DELETE
  semantics).
- Other audit findings (covered by separate PRs/branches).
