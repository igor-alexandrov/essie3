# `handleCopyObject` error mapping — design

## Goal

Close audit critical finding #2: `handleCopyObject` currently maps
every error from `Storage.CopyObject` to `404 NoSuchKey`, regardless
of cause. After this change, the three distinct failure modes return
distinct, S3-shaped responses:

| Cause                                         | Response                              |
| --------------------------------------------- | ------------------------------------- |
| Invalid bucket or key name (src or dst)       | `400 InvalidArgument`                 |
| Source object does not exist                  | `404 NoSuchKey` (with src identifiers)|
| Anything else (mkdir, marshal, IO, ...)       | `500 InternalError`                   |

## Background

`Storage.CopyObject(srcBucket, srcKey, dstBucket, dstKey)` calls
`GetObject(src)` then `PutObject(dst)`. Both internally call
`validateName` and can return `errInvalidName`; `GetObject` can also
return `os.ErrNotExist` from `os.ReadFile`; `PutObject` can return
wrapped IO errors from `os.MkdirAll` / `writeFileAtomic` /
`json.MarshalIndent`. The current handler swallows the distinction.

Concrete user-visible bugs:

- `PUT /b/dst` with `x-amz-copy-source: /b/../escape` →
  Storage returns `errInvalidName`, handler responds `404 NoSuchKey`
  pointing at the wrong path.
- `PUT /b/../escape` with valid copy-source → same as above with the
  destination invalid; handler still responds 404.
- A genuine source-not-found returns `404 NoSuchKey` (correct, but
  by accident — the "everything is 404" path).

## Architecture

Single change site: the error branch in `handleCopyObject`
(`handler.go:310-313`). Replace the unconditional `writeNoSuchKey`
with a `switch` over `errors.Is`:

```go
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
```

`errors.Is` walks any `%w`-wrapped chain, so wrapping inside
`Storage.CopyObject` (or downstream) is preserved if it appears
later. `errInvalidName` is the sentinel already declared in
`storage.go`; `os.ErrNotExist` is the standard library's. No new
imports beyond `errors` and `os`, both of which `handler.go` already
imports.

## Identifier disambiguation

`errInvalidName` doesn't carry information about whether the bad
name was src or dst (both go through Storage's `validateName`). The
400 response identifies the destination (`dstBucket`/`dstKey`); the
message string from `errInvalidName.Error()` is
`"invalid bucket or key name"`. For a dev/test stand-in this is
acceptable — the test runner sees a 400 and a self-explanatory
message, and the URL of the request makes the destination obvious.

If a future caller needs precise src-vs-dst distinction, the cleanest
extension is to wrap inside `Storage.CopyObject`:
`fmt.Errorf("source: %w", err)` vs `fmt.Errorf("destination: %w",
err)`. Out of scope today.

## Other scenarios in `handleCopyObject` that are NOT touched

- The `len(parts) != 2` early return at line 304: already returns
  `400 InvalidArgument` for a malformed `x-amz-copy-source` header
  shape. Unchanged.
- The auth check happens upstream in `handleObject` (line 100,
  `case http.MethodPut`). The handler reaches `handleCopyObject` only
  after successful auth. Unchanged.

## Testing

Three new integration tests in `handler_test.go` exercising the three
response shapes through the real HTTP server:

- `TestHandler_CopyObject_InvalidSourceReturns400` — PUT a real source
  object first; then issue a copy with `x-amz-copy-source:
  /b/../escape`. Assert status 400 and the response body contains
  `<Code>InvalidArgument</Code>`.
- `TestHandler_CopyObject_MissingSourceReturns404` — issue a copy
  with `x-amz-copy-source: /b/missing-key.txt` (no real source
  uploaded). Assert status 404 and the body contains
  `<Code>NoSuchKey</Code>` and `<Key>missing-key.txt</Key>`.
- `TestHandler_CopyObject_Succeeds` (or extension of the existing
  `TestHandler_CopyObject`): unchanged success path; just confirms
  the new error switch doesn't break the happy path.

The pre-existing `TestHandler_CopyObject` already covers the happy
path; we'll add the two new failure-path tests beside it. No changes
to the existing test.

## File changes summary

- **Modify** `handler.go` — replace the error branch in
  `handleCopyObject`. Add `"errors"` and `"os"` to the import block
  if not already present (verify before editing — both are likely
  already there).
- **Modify** `handler_test.go` — append two new tests for the 400 and
  404 branches.

## Out of scope

- Wrapping errors inside `Storage.CopyObject` to distinguish src vs
  dst (would extend this beyond a one-site fix; not needed for the
  audit's correctness criterion).
- The audit's separate finding about `handleCopyObject` not
  consulting the source object's ACL on copy (covered as "by design"
  in the audit itself; no test or doc gap here).
- README/CLAUDE.md updates — neither file describes the COPY error
  mapping today, so there's nothing user-facing to update.
