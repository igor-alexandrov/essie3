# Range request support — design

## Goal

Implement HTTP `Range` request handling on essie3 so that clients can
fetch byte slices of stored objects and fallback placeholders. essie3
currently returns the full body on every GET/HEAD; under this design,
range-aware clients (browsers playing video, curl `--continue-at`,
range-using S3 SDKs) get the partial responses they expect.

The feature is purely additive — requests without a `Range` header
behave exactly as they do today.

## Scope

- Real stored objects: full Range support.
- Fallback placeholders: full Range support (browsers seek into
  fallback video by issuing Range requests on `<video>` tags).
- Single-range only (`bytes=N-M`, `bytes=N-`, `bytes=-N`). Multi-range
  is ignored (serve full body, per RFC 9110 §14.2.1's
  permissive-server clause).
- `If-Range` is supported with ETag matching. Date-based matching is
  out of scope.
- The storage layer is unchanged — slicing happens on the buffered
  body that `GetObject` already returns. Streaming for huge objects is
  future work.

## Architecture

A new file `range.go` exports:

```go
// byteRange is a closed [start, end] interval in bytes.
type byteRange struct {
    start, end int64
}

// rangeOutcome is what evaluateRange tells the caller to do.
// Exactly one of these three states applies:
//
//   serveFull=true                 → return 200 with the full body
//   serveFull=false, bounds != nil → return 206 with bounds
//   serveFull=false, bounds == nil → return 416
type rangeOutcome struct {
    serveFull bool
    bounds    *byteRange
}

// evaluateRange parses Range and If-Range against a representation of
// length totalLen and ETag etag, and returns the action the caller
// should take.
func evaluateRange(r *http.Request, totalLen int64, etag string) rangeOutcome
```

A new helper in `xml.go`:

```go
// writeInvalidRange writes the S3-shaped 416 response. Content-Range
// is set first so the value survives the WriteHeader call in
// writeXMLError.
func writeInvalidRange(w http.ResponseWriter, bucket, key string, totalLen int64)
```

`handler.go` changes at four call sites — the four code paths that
currently write a body or HEAD response for an object or a fallback:

- `handleGetObject` real-object path
- `handleGetObject` fallback path
- `handleHeadObject` real-object path
- `handleHeadObject` fallback path

Each call site computes `out := evaluateRange(...)` after auth, sets
`Accept-Ranges: bytes` and the existing object/fallback headers, then
branches on `out` to write 200 / 206 / 416.

Storage (`storage.go`) is **not** changed.

## Range parsing rules

`evaluateRange` walks the request in this order and returns at the
first matching case:

1. No `Range` header → `serveFull=true`.
2. `Range` value does not start with `bytes=` → `serveFull=true`.
   (RFC 9110: ignore unknown range units.)
3. `Range` value (after `bytes=`) contains a comma →
   `serveFull=true`. Multi-range is not supported.
4. If `If-Range` header is present:
   - If the literal string equals the object's `etag` (including the
     surrounding quotes), continue.
   - Otherwise → `serveFull=true`.
5. Parse the single spec, which must match `N-M`, `N-`, or `-N` where
   the present sides are non-negative decimal integers. Anything that
   doesn't fit (`bytes=`, `bytes=abc`, `bytes=-`, missing dash, etc.)
   → `serveFull=true`.
6. Compute bounds:
   - `N-M` → `start=N, end=M`. Unsatisfiable if `N > M` or
     `N >= totalLen` (range starts past the end of the
     representation).
   - `N-` (open-ended) → `start=N, end=totalLen-1`. Unsatisfiable if
     `N >= totalLen`.
   - `-N` (suffix length) →
     - If `N <= 0` → unsatisfiable.
     - If `N >= totalLen` → `start=0, end=totalLen-1` (whole body).
     - Otherwise → `start=totalLen-N, end=totalLen-1`.
7. Clamp `end` to `totalLen-1` if `end >= totalLen`. (RFC: end
   beyond the representation length is the last byte.)
8. If `totalLen == 0`, every range is unsatisfiable.

Outcomes:

- Step 1–4 hit → `serveFull=true`.
- Step 5 produces an unparseable spec → `serveFull=true`.
- Step 6 / step 8 produces unsatisfiable → `serveFull=false, bounds=nil`.
- Otherwise → `serveFull=false, bounds={start, end}`.

## Response shape

### Common headers on every object/fallback GET and HEAD response

- `Accept-Ranges: bytes` — set on 200, 206, and HEAD. Not set on
  auth-error (403) or not-found (404) responses; those don't
  represent ranged content.
- `Content-Type`, `ETag` (real objects only), `Last-Modified` (real
  objects only), `Content-Disposition` (real objects when set;
  fallbacks via `Fallback.Disposition`) — exactly as today.

### 200 (full body)

`Content-Length: <totalLen>`, body is the full byte slice. This is
the existing code path; the only addition is `Accept-Ranges`.

### 206 (partial)

- `Content-Range: bytes <start>-<end>/<totalLen>`
- `Content-Length: <end - start + 1>`
- Body is `obj.Body[start : end+1]` (or `p.Body[start : end+1]` for
  fallbacks).
- HEAD: same headers, no body.

### 416 (unsatisfiable)

Via `writeInvalidRange(w, bucket, key, totalLen)`:

- `Content-Range: bytes */<totalLen>` (set before `WriteHeader`)
- `Content-Type: application/xml` (set by `writeXMLError`)
- Status `416 Requested Range Not Satisfiable`
- Body is the standard S3 XML error shape with
  `Code=InvalidRange` and
  `Message="The requested range is not satisfiable"`.

### Fallback placeholders and `If-Range`

Fallback responses have no ETag today. Per the parsing rules, an
`If-Range` header on a fallback request always fails the match step
and falls through to `serveFull=true`. This is an acceptable behavior
trade-off: clients resuming an interrupted fallback download fetch
the full body again. Placeholders are throw-away assets, so this is
not a correctness issue.

### Minor cleanup

The existing `handleGetObject` writes `Content-Length` via
`fmt.Sprintf("%d", obj.Meta.ContentLength)`. While editing this
section the new code uses `strconv.FormatInt(totalLen, 10)` for the
three new `Content-Length` writes; the existing line is updated to
the same form for consistency. No behavioral change.

## File changes summary

- **Create** `range.go` — `byteRange`, `rangeOutcome`, `evaluateRange`.
- **Create** `range_test.go` — unit tests for `evaluateRange`.
- **Modify** `xml.go` — add `writeInvalidRange` helper.
- **Modify** `handler.go` — four call sites in `handleGetObject` and
  `handleHeadObject`; add `strconv` to the import block.
- **Modify** `handler_test.go` — integration tests covering all
  outcomes (200 + Accept-Ranges, 206 with each Range form, 416, HEAD
  + Range, If-Range match and mismatch, fallback Range).

## Testing

### Unit tests in `range_test.go`

Pure-function tests against `evaluateRange`, no server:

- No `Range` header → `serveFull=true`.
- Unknown unit (`Range: items=0-10`) → `serveFull=true`.
- Multi-range (`Range: bytes=0-10, 20-30`) → `serveFull=true`.
- Malformed values one case per rule: `bytes=`, `bytes=abc`,
  `bytes=-` (both sides empty) → `serveFull=true`; `bytes=5-3` (N>M)
  → unsatisfiable.
- Valid `bytes=N-M`, `bytes=N-`, `bytes=-N` against a known totalLen
  → correct bounds.
- Edge: `bytes=0-0` on len=1 → bounds `{0,0}`.
- Edge: `bytes=-N` with `N >= totalLen` → bounds `{0, totalLen-1}`.
- Edge: `bytes=-0` → unsatisfiable.
- Edge: `bytes=N-M` with `M >= totalLen` (but `N < totalLen`) →
  bounds clamped to `{N, totalLen-1}`.
- Edge: `bytes=N-M` with `N >= totalLen` → unsatisfiable.
- Edge: `totalLen=0` → unsatisfiable for any range.
- `If-Range` matches ETag → range honored.
- `If-Range` differs from ETag → `serveFull=true`.
- `If-Range` is a date (no `"`s) → treated as mismatch,
  `serveFull=true`.

### Integration tests in `handler_test.go`

Through the existing `httptest.NewServer(NewHandler(...))` harness:

- `Accept-Ranges: bytes` present on GET 200 of a stored object.
- `Accept-Ranges: bytes` present on HEAD 200 of a stored object.
- GET with `Range: bytes=0-4` on an 11-byte object → 206,
  `Content-Range: bytes 0-4/11`, `Content-Length: 5`, body matches
  the slice.
- GET with `Range: bytes=-3` → 206 with the last 3 bytes.
- GET with `Range: bytes=5-` → 206 from byte 5 to end.
- GET with `Range: bytes=1000-2000` on a small object → 416, response
  body is S3 XML with `Code=InvalidRange`, `Content-Range: bytes
  */<total>` is set.
- HEAD with `Range: bytes=0-4` → 206, headers set, response body
  empty.
- GET with `If-Range: "<correct-etag>"` + valid `Range` → 206.
- GET with `If-Range: "wrong-etag"` + valid `Range` → 200 with full
  body and no `Content-Range`.
- GET on a fallback placeholder with `Range: bytes=0-9` → 206 +
  correctly sliced fallback bytes + `Accept-Ranges: bytes`.
- HEAD on a fallback placeholder → `Accept-Ranges: bytes` present.

Existing GET/HEAD tests that don't send a `Range` header continue to
pass unchanged; the new code adds `Accept-Ranges` but does not alter
status codes or body bytes for non-ranged requests. Any test that
asserts on the exact header set will be tightened during
implementation.

## Documentation

Add a "Range requests" sub-section to `README.md` (in the Features
list and as a short paragraph after the Usage examples) noting that
GET/HEAD support single-range `Range`, `If-Range` ETag matching, and
that 416 responses are S3-shaped XML errors.

## Out of scope

- Multi-range (`Range: bytes=0-100, 200-300` → `multipart/byteranges`).
- Streaming the slice rather than buffering the whole body in
  memory.
- `If-Range` with HTTP-date matching.
- `If-Modified-Since` / `If-None-Match` conditional GET semantics
  (separate from Range and not part of this feature).
