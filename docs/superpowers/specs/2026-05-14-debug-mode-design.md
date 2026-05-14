# DEBUG mode — design

## Goal

Add an opt-in DEBUG mode that prints request and response details
(method, path, headers, status, timing) to make it easier to debug
integration tests against essie3 — especially auth-failure paths where
seeing the actual `Authorization` header is the whole point.

## Activation

A new env var:

| Variable        | Default | Description                                  |
| --------------- | ------- | -------------------------------------------- |
| `ESSIE3_DEBUG`  | *unset* | When set to `true`, log full request/response details to stderr. |

Naming follows the existing `ESSIE3_*` prefix (`ESSIE3_ACCESS_KEY`,
`ESSIE3_FALLBACK_PUBLIC`). Any value other than the literal string
`true` is treated as disabled — same pattern as `ESSIE3_FALLBACK_PUBLIC`
in `main.go`.

## Architecture

A new file `debug.go` exports an HTTP middleware. `main.go` wraps the
core handler with it only when `ESSIE3_DEBUG=true`:

```go
var h http.Handler = NewHandler(storage, fallback, auth)
if debug {
    h = WithDebugLogging(h, os.Stderr)
}
srv := &http.Server{Handler: h, ...}
```

The core `Handler` stays unchanged — observability lives in its own
layer.

### Components in `debug.go`

- `WithDebugLogging(next http.Handler, out io.Writer) http.Handler` —
  middleware constructor. `out` is the destination for debug output;
  `main.go` passes `os.Stderr`, tests pass a `bytes.Buffer`.
- `debugResponseWriter` — wraps `http.ResponseWriter`. Captures the
  status code (default `200` if `WriteHeader` is never called), and
  the number of bytes written via `Write`. Delegates `Header()`,
  `Write()`, `WriteHeader()` to the inner writer so the response is
  unchanged from the client's perspective.
- `formatRequest(*http.Request)` / `formatResponse(*debugResponseWriter,
  time.Duration)` — produce the multi-line strings (pure functions,
  easy to unit-test).

### Request flow when DEBUG is on

1. Middleware records `start := time.Now()`.
2. Middleware prints the request block to stderr.
3. Middleware constructs a `debugResponseWriter` and calls
   `next.ServeHTTP(drw, r)`.
4. Middleware prints the response block with `time.Since(start)` and
   `drw.bytes`.

### Startup banner

`main.go` prints one extra line when DEBUG is enabled, alongside the
existing `auth:` line:

```
  debug:    enabled
```

### Removal of the existing per-request log line

`handler.go:25` currently does `log.Printf("%s %s", r.Method,
r.URL.Path)`. This line is removed — its content is a strict subset of
the DEBUG request block. Without removal, every request would be
logged twice when DEBUG is on.

## Output format

Output goes to the `io.Writer` supplied to `WithDebugLogging` (stderr
in production, a `bytes.Buffer` in tests). Internally the middleware
holds a `*log.Logger` constructed as `log.New(out, "", 0)` (zero flags
= no timestamp prefix). Using `log.Logger` rather than `fmt.Fprint`
matters: `log.Logger` serializes writes with an internal mutex, so
concurrent requests can't interleave their multi-line debug blocks.
The startup banner continues to use stdout via `fmt.Printf`.

Each block is assembled into a single string (e.g. with
`strings.Builder`) and written with one `logger.Print(...)` call so the
whole block lands on stderr atomically.

A single request/response pair is rendered as two blocks:

```
--> PUT /mybucket/photos/photo.jpg
    Host: localhost:9000
    Authorization: AWS4-HMAC-SHA256 Credential=AKIATEST/20260514/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=abc...
    Content-Type: image/jpeg
    Content-Length: 48213
    X-Amz-Acl: public-read
<-- 200 OK (12.3ms, 0 bytes)
    Etag: "d41d8cd98f00b204e9800998ecf8427e"
```

Rules:

- Request line: `--> METHOD PATH` where PATH is `r.URL.RequestURI()`
  (preserves query string).
- Response line: `<-- CODE STATUS_TEXT (elapsed, N bytes)`.
  `STATUS_TEXT` comes from `http.StatusText(code)`.
  `elapsed` is formatted via `time.Duration.String()` (e.g. `12.3ms`).
- Headers are printed sorted alphabetically by header name, indented 4
  spaces, in `Name: value` form. Sorting gives stable output for test
  log diffs. Header names are rendered in canonical MIME form (Go's
  `http.Header` already stores them that way).
- Multi-value headers: one indented line per value (Go's `Header` is a
  `map[string][]string`; iterate the slice).
- All headers are printed verbatim, including `Authorization`. No
  redaction. The user explicitly chose this — for a local dev/test
  tool, the access key in the SigV4 `Credential=` field is what you
  want to see when debugging.
- Bodies are **not** logged. Many essie3 requests carry binary
  payloads (images, PDFs, videos) that would flood the log.
- Indent: 4 spaces.

## Testing

A new file `debug_test.go` covers:

- `debugResponseWriter` records status (including the implicit-200 case
  when no `WriteHeader` is called) and byte count.
- `formatRequest` produces the expected multi-line string for a
  representative request (method, path with query string, sorted
  headers, multi-value header rendered as multiple lines).
- `formatResponse` produces the expected multi-line string for a
  representative response.
- End-to-end: with the middleware installed (configured to write to
  an injected `io.Writer` rather than `os.Stderr` so the test can
  capture it), hitting a stub handler produces both blocks in the
  right order and the wrapped handler still sees the original request
  and writes through to the underlying `ResponseWriter` correctly.

No changes to existing tests are expected — the middleware is only
installed when `ESSIE3_DEBUG=true`, which the existing test suite does
not set.

## Documentation

Add one row to the env-var table in `README.md`:

```
| `ESSIE3_DEBUG` | *(unset)* | When set to `true`, log full request and response details (method, path, headers, status, timing) to stderr. Useful when debugging integration tests, especially auth-failure paths. Off by default. |
```

## Out of scope

- Body logging (binary payloads make this noisy; can be added later
  under a separate flag if needed).
- Header redaction (Authorization is shown in full by design).
- Structured (JSON) logging.
- Log levels / `log/slog` migration.
- Per-route or per-method filtering.
