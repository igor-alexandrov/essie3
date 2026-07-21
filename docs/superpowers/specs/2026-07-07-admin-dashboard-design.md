# Admin dashboard — design

## Goal

Give the operator a **read-only** window into a running essie3: what
objects exist, live request traffic, and rollup stats. Today the only
visibility is `ESSIE3_DEBUG=true`, which fire-hoses request/response
blocks to stderr — good for a single request, useless for "what's in
bucket X right now" or "what's my fallback hit rate."

The dashboard is a small web UI served on a **separate port**, so the
S3 surface on `:9000` is untouched (no route can collide with a real
bucket name, no auth entanglement). It is strictly observational:
every route is `GET`, nothing mutates state. Upload, delete, and
runtime configuration are explicit **non-goals** (see Out of scope).

It is **multi-page**: the dashboard (`/`) shows stats, the live traffic
feed, and the list of buckets — each bucket name links to its own
standalone page (`/buckets/{name}`) listing that bucket's objects. Every
page is **live**: the dashboard's bucket list soft-refreshes when a
write/delete arrives on the SSE stream, and a bucket page soft-refreshes
its own object table when a write/delete for *that* bucket arrives. Both
use the same mechanism — a debounced re-fetch of an HTML fragment
(`/fragment` for the dashboard, `/buckets/{name}/fragment` for a bucket)
that swaps the `#content` region — so a newly PUT object appears without
a manual reload. A shared page shell (header with the "Live" indicator,
plus the feed on the dashboard) and one vanilla-JS `<script>` serve both
page types; the object rows live on the bucket page, not the dashboard.

Like the rest of essie3 the *code* is pure stdlib — `net/http`,
`html/template`, `embed`, and Server-Sent Events via `http.Flusher`.
Styling comes from a **single vendored classless CSS framework** —
the classless build of [Pico.css](https://github.com/picocss/pico)
(`pico.classless.min.css`, MIT, ~80 KB minified, served over loopback
so size is irrelevant) — committed into the repo and served via
`//go:embed`. The **classless** build is deliberate: it styles plain
semantic HTML (`<table>`, `<nav>`, `<header>`, auto-applied page
container) with **no utility classes, no `.container` wrapper, and no
build step** — the same reason
a CDN or a Tailwind toolchain is rejected: both break offline-dev, and
Tailwind additionally needs a Node build whose purge step silently
breaks styling when markup gains a class. The vendored file is a static
asset like the placeholder JPEGs already in the repo; it adds **zero Go
dependencies** (nothing in `go.mod`, `go mod tidy` unaffected). The
only page JS is one `EventSource` call for the live feed.

## Scope

- New opt-in env var `ESSIE3_ADMIN_PORT`. Unset/empty → dashboard
  disabled (zero behavior change for existing users). Set to a port
  number → a second `http.Server` starts, bound to **loopback only**
  (`127.0.0.1:<port>`), because the admin surface has no auth.
- A **dashboard page** (`GET /`), server-rendered:
  - **Stats strip** — bucket count, total objects, total bytes,
    fallback hit rate, uptime.
  - **Live traffic feed** — a table that streams new requests as they
    arrive.
  - **Buckets** — every bucket with object count and total size; the
    name links to that bucket's standalone page. No object rows here.
- A **bucket page** (`GET /buckets/{name}`), server-rendered: the
  bucket's object table (key, size, content-type, ACL, created-at), a
  back link to the dashboard, and the shared "Live" header. An unknown
  bucket returns a 404 HTML page (not the S3 XML error shape).
- Data / fragment endpoints:
  - **`/events`** — an SSE stream. On connect it replays the in-memory
    ring buffer (recent history) then pushes each new request live.
  - **`/fragment`** — the dashboard's stats + bucket-list region as an
    HTML fragment (no page chrome), for soft-refresh.
  - **`/buckets/{name}/fragment`** — one bucket's object-table region
    as an HTML fragment, for that bucket page's soft-refresh.
- **Soft auto-refresh.** Each page opens one `EventSource('/events')`.
  On the dashboard the feed appends a row per event; when an event's
  outcome is `write` or `delete` it schedules a debounced (~750 ms)
  `fetch('/fragment')` and swaps `#content`. A bucket page has no feed
  table; it refreshes only when a `write`/`delete` event's bucket
  matches, re-fetching `/buckets/{name}/fragment`. Reads never trigger
  a refresh (they don't change the filesystem).
- Live traffic is captured by a middleware wrapping the S3 handler,
  structurally identical to `WithDebugLogging`: it records one
  `TrafficEvent` per request into an in-process broker. Each request's
  **outcome** (real object / fallback / miss / denied / write /
  delete / other) is derived from the response status plus the
  `X-Essie3-Fallback` header the handler sets on fallback responses.
- Stats: per-bucket object count and total bytes (from a filesystem
  walk), fallback hit rate (from broker counters), and process uptime.
- The dashboard reflects the live filesystem: each render of `/` or
  `/fragment` walks `data/` fresh (no caching, no index). At dev scale
  a walk per write-triggered refresh is cheap; the ~750 ms debounce
  coalesces write bursts into a single walk.

## Non-goals for v1

Listed here because the scope was deliberately trimmed:

- **No upload / no delete / no runtime config** — every route is
  `GET`. This is the primary scope decision.
- **No object viewing/download through the admin UI** — the dashboard
  lists metadata only. To fetch an object, hit its S3 URL directly.
- **No auth on the admin port** — loopback binding is the containment.
- **No persistence of traffic** — the event ring buffer is in-memory
  and resets on restart.

## Architecture

Two new files (`traffic.go`, `admin.go`), plus read-only additions to
`storage.go` and a one-header change to `handler.go`. The S3
`Handler` type and `NewHandler` signature are unchanged.

### New file: `traffic.go`

The event model, the in-memory broker (ring buffer + live
subscribers), and the capture middleware.

```go
package main

import (
    "net/http"
    "strings"
    "sync"
    "time"
)

// TrafficEvent is one observed request, published to the broker by the
// capture middleware and rendered in the live feed.
type TrafficEvent struct {
    Seq     uint64    // monotonic, assigned by the broker; SSE id
    Time    time.Time
    Method  string
    Bucket  string
    Key     string    // "" for bucket-level requests
    Status  int
    Bytes   int
    Outcome string    // see classifyOutcome
}

// TrafficBroker keeps the last N events in a ring buffer and fans new
// events out to live SSE subscribers. Safe for concurrent publish and
// subscribe.
type TrafficBroker struct {
    mu       sync.Mutex
    ring     []TrafficEvent // len<=cap, oldest-first once full
    cap      int
    seq      uint64
    subs     map[chan TrafficEvent]struct{}
    // counters for the stats page
    reads    uint64 // GET/HEAD on a key that missed stored object
    fallbacks uint64 // subset of reads served by a fallback
}

func NewTrafficBroker(capacity int) *TrafficBroker

// Publish assigns Seq, appends to the ring (evicting oldest), bumps
// counters, and non-blockingly sends to every subscriber. A subscriber
// whose buffered channel is full drops the event rather than blocking
// the request path.
func (b *TrafficBroker) Publish(e TrafficEvent)

// Subscribe returns the current ring snapshot (for backlog replay) and
// a channel of future events, plus an unsubscribe func the SSE handler
// defers.
func (b *TrafficBroker) Subscribe() (backlog []TrafficEvent, ch chan TrafficEvent, cancel func())

// Stats returns the counters the dashboard needs.
func (b *TrafficBroker) Stats() (reads, fallbacks uint64)
```

The capture middleware reuses the status/bytes-capturing
`ResponseWriter` pattern from `debug.go`. Rather than duplicate
`debugResponseWriter`, promote it to a shared, neutrally-named
`countingResponseWriter` (rename in `debug.go`, keep its behavior
byte-for-byte) that both middlewares use.

```go
// WithTrafficCapture wraps next so every request is published to the
// broker after it completes. Independent of WithDebugLogging; both may
// wrap the same handler in either order.
func WithTrafficCapture(next http.Handler, broker *TrafficBroker) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        crw := newCountingResponseWriter(w)
        next.ServeHTTP(crw, r)
        bucket, key := splitBucketKey(r.URL.Path)
        broker.Publish(TrafficEvent{
            Time:    start,
            Method:  r.Method,
            Bucket:  bucket,
            Key:     key,
            Status:  crw.status,
            Bytes:   crw.bytes,
            Outcome: classifyOutcome(r.Method, crw.status, crw.Header().Get("X-Essie3-Fallback")),
        })
    })
}

// splitBucketKey mirrors ServeHTTP's own /bucket/key split so the feed
// labels match how the handler routed the request.
func splitBucketKey(path string) (bucket, key string)

// classifyOutcome maps (method, status, fallback-header) to a short
// label for the feed:
//   fallback header non-empty         -> "fallback"
//   status 403                        -> "denied"
//   GET/HEAD, status 404              -> "miss"
//   GET/HEAD, 2xx/206/304             -> "real"
//   PUT/POST, 2xx                     -> "write"
//   DELETE, 2xx                       -> "delete"
//   anything else                     -> "other"
func classifyOutcome(method string, status int, fallback string) string
```

Counter semantics for the hit-rate stat: a GET/HEAD whose outcome is
`miss` or `fallback` increments `reads`; `fallback` additionally
increments `fallbacks`. Hit rate = `fallbacks / reads` (0 when
`reads == 0`). This is computed in `Publish` from the already-derived
outcome, so there's a single source of truth.

### New file: `admin.go`

The admin `http.Handler` (a `*http.ServeMux`), the single-page handler,
the fragment handler, the SSE handler, and the embedded template +
stylesheet.

Static assets are embedded at the repo root so the layout stays flat
(no `templates/` package subdir with `.go` files — the embed targets
are plain assets):

```go
//go:embed admin.html.tmpl
var adminTemplate string // one file; base page + reusable "content" block

//go:embed pico.classless.min.css
var adminCSS []byte // served at GET /assets/pico.classless.min.css
```

`go:embed` resolves at compile time, so these assets must be present in
the build context. Two standing constraints follow: (1) the `Dockerfile`
uses a **selective** `COPY *.go ./`, so it must also
`COPY admin.html.tmpl pico.classless.min.css ./` before `go build` —
otherwise the container build fails with `pattern admin.html.tmpl: no
matching files found`; and (2) never add `*.tmpl`/`*.css` to
`.dockerignore`. Both are verified by a `docker build` in the final
plan task.

```go
package main

import (
    "embed"
    "html/template"
    "net/http"
    "time"
)

type AdminServer struct {
    storage   *Storage
    fallback  *Fallback
    broker    *TrafficBroker
    startedAt time.Time
    tmpl      *template.Template // parsed adminTemplate: page + "content"
}

// NewAdminServer parses the template once and returns a ready handler.
func NewAdminServer(s *Storage, fb *Fallback, b *TrafficBroker, startedAt time.Time) *AdminServer

// Handler wires the routes onto a ServeMux:
//   GET /                          -> dashboard (stats + feed + bucket links)
//   GET /fragment                  -> dashboard stats+buckets fragment
//   GET /buckets/{name}            -> one bucket's standalone page
//   GET /buckets/{name}/fragment   -> that bucket's object-table fragment
//   GET /events                    -> SSE stream (backlog + live)
//   GET /assets/pico.classless.min.css   -> embedded stylesheet (long cache)
//   GET /healthz                   -> 200 "ok" (liveness, plain text)
// Any non-GET -> 405. Unknown path (incl. unknown bucket) -> 404 HTML.
func (a *AdminServer) Handler() http.Handler
```

The template is `html/template` (not `text/template`) so bucket/key
names — attacker-influenceable via the S3 API — are HTML-escaped by
default. A **single** `admin.html.tmpl` defines the full page (which
links the vendored stylesheet and holds the inline feed `<script>`) and
a nested `{{define "content"}}…{{end}}` block for the stats-strip +
buckets region. `GET /` executes the whole template; `GET /fragment`
executes only the `content` block — so the initial render and every
soft-refresh come from the **same markup**, no drift. Both handlers
compute the same view model (`ListBuckets` + per-bucket `ListObjects`,
`broker.Stats()` → hit rate, `time.Since(startedAt)` uptime). The
markup is semantic only — the classless framework styles bare elements,
so there are no `class` attributes to maintain. The CSS route serves
`adminCSS` with a far-future `Cache-Control` (immutable per build).

The SSE handler:

```go
func (a *AdminServer) handleEvents(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok { http.Error(w, "streaming unsupported", http.StatusInternalServerError); return }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    backlog, ch, cancel := a.broker.Subscribe()
    defer cancel()
    for _, e := range backlog { writeSSE(w, e) }
    flusher.Flush()

    for {
        select {
        case e := <-ch:
            writeSSE(w, e); flusher.Flush()
        case <-r.Context().Done():
            return // client disconnected
        }
    }
}
```

`writeSSE` renders one event as an `id:`/`data:` frame. The `data:`
payload is a small JSON object (`encoding/json`) so the browser JS can
append a table row without re-parsing text — the only place `admin.go`
emits JSON.

### Modifications to `storage.go`

Two read-only listing methods plus their result types. These walk the
filesystem directly; they take **no per-key locks** — the dashboard is
best-effort and a momentarily torn/in-flight object is acceptable, so
a listing must never block or fail the S3 write path. A key whose
`.meta.json` is missing or unparseable is still listed, with metadata
fields left zero rather than dropping the row or erroring the page.

```go
// BucketInfo is one bucket's summary row (the <details> summary).
type BucketInfo struct {
    Name        string
    ObjectCount int
    TotalBytes  int64
}

// ObjectInfo is one object row inside a bucket's <details> table.
type ObjectInfo struct {
    Key         string    // forward-slash key, sidecars excluded
    Size        int64     // meta.ContentLength, else on-disk body size
    ContentType string
    ACL         string
    CreatedAt   time.Time // zero if meta missing/unparseable
}

// ListBuckets returns every immediate subdirectory of dataDir with its
// object count and total size, sorted by name. Missing dataDir -> empty
// slice, nil error.
func (s *Storage) ListBuckets() ([]BucketInfo, error)

// ListObjects walks data/<bucket> recursively, pairing each object with
// its .meta.json sidecar (which is itself never listed). Keys are
// reconstructed with "/" separators relative to the bucket root, sorted.
// Unknown bucket -> nil slice, os.ErrNotExist.
func (s *Storage) ListObjects(bucket string) ([]ObjectInfo, error)
```

The sidecar filter keys off the same `.meta.json` suffix that
`metaPath` appends, so the two stay in lockstep. `ListObjects`
validates `bucket` via the existing `validateName` before touching the
filesystem, consistent with every other storage entrypoint.

### Modifications to `handler.go`

The single signal the traffic middleware needs: set
`X-Essie3-Fallback` on the two fallback branches, valued by which
fallback flavor served the bytes.

- In `handleGetObject`'s fallback block ([handler.go:180]) and
  `handleHeadObject`'s fallback block ([handler.go:265]), add:

  ```go
  if p.Generated {
      w.Header().Set("X-Essie3-Fallback", "generate")
  } else {
      w.Header().Set("X-Essie3-Fallback", "pool")
  }
  ```

- Add `X-Essie3-Fallback` to the `Access-Control-Expose-Headers` list
  in `setCORS` so browser-based S3 clients can also observe it.

The header is only ever set on fallback responses, so real-object and
error responses are byte-for-byte unchanged apart from the CORS
expose-list widening (which lists a header, it doesn't add one).

### Modifications to `main.go`

Capture the start time, and — only when `ESSIE3_ADMIN_PORT` is set —
build the broker, wrap the handler with `WithTrafficCapture`, and start
the loopback admin server in a goroutine alongside the existing S3
server. The admin server shares the graceful-shutdown path.

```go
startedAt := time.Now()
// ... existing storage/fallback/auth wiring ...

var broker *TrafficBroker
if adminPort := os.Getenv("ESSIE3_ADMIN_PORT"); adminPort != "" {
    broker = NewTrafficBroker(trafficRingSize) // const, 500
    // WithTrafficCapture wraps the base S3 handler; debug logging (if
    // enabled) wraps the result, or vice versa — order is immaterial.
    handler = WithTrafficCapture(handler, broker)
}
// ... debug wrap, build srv as today ...

if broker != nil {
    admin := NewAdminServer(storage, fallback, broker, startedAt)
    adminSrv := &http.Server{
        Addr:              "127.0.0.1:" + os.Getenv("ESSIE3_ADMIN_PORT"),
        Handler:           admin.Handler(),
        ReadHeaderTimeout: 10 * time.Second,
        // No WriteTimeout: it would cut off long-lived SSE streams.
    }
    go func() {
        if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            log.Printf("admin server error: %v", err)
        }
    }()
    // adminSrv.Shutdown(shutdownCtx) added to the existing shutdown block.
}
```

Startup banner gains one line when enabled:

```
  admin:    http://127.0.0.1:<port>
```

Note the admin server has **no `WriteTimeout`** — the S3 server's
5-minute `WriteTimeout` would sever SSE connections. `ReadHeaderTimeout`
still guards against slow-loris on the admin port.

## Configuration

### `ESSIE3_ADMIN_PORT`

| Value        | Effect                                              |
|--------------|-----------------------------------------------------|
| unset / `""` | Admin dashboard disabled (default). No second port. |
| e.g. `9001`  | Dashboard served on `<ESSIE3_ADMIN_HOST>:9001`.     |

### `ESSIE3_ADMIN_HOST`

Interface the admin server binds; default `127.0.0.1`. The surface is
unauthenticated, so loopback is the safe default for a bare `go run` on
a dev machine.

**Container caveat:** a process bound to `127.0.0.1` *inside* a container
is unreachable through a Docker published port — Docker forwards host
traffic to the container's external interface, not its loopback. To use
the dashboard from a container, set `ESSIE3_ADMIN_HOST=0.0.0.0`; the
container boundary and the `ports:` mapping then govern exposure. The S3
server already binds all interfaces (`:PORT`), which is why it works in
Docker unchanged. The admin `Addr` is `net.JoinHostPort(adminHost, adminPort)`.

### Interaction with existing vars

- `ESSIE3_DEBUG` — orthogonal. Both may be on; the two middlewares are
  independent. Debug still writes text blocks to stderr; the dashboard
  reads structured events from the broker.
- Auth vars (`ESSIE3_ACCESS_KEY`, `ESSIE3_FALLBACK_PUBLIC`) — the
  admin port ignores them entirely (it never touches the S3 auth path).
  Denied S3 requests still surface in the feed with outcome `denied`.

## Response / view shapes

### Single page (`GET /`)

Server-rendered HTML: `<head>` links the stylesheet; `<body>` holds the
stats strip and buckets region (the `content` block) followed by an
initially-empty feed `<table>` and an inline `<script>`. The script:

1. Opens `new EventSource('/events')`.
2. On each message, prepends a feed row (`time · method · bucket/key ·
   status · outcome`) and caps the DOM at ~200 rows.
3. If the event's `outcome` is `write` or `delete`, (re)starts a
   ~750 ms debounce timer; on fire, `fetch('/fragment')` and replace
   the content region's `innerHTML` with the response text.

The whole script is a couple dozen lines of vanilla JS — no framework,
no dependencies.

### Fragment (`GET /fragment`)

The `content` block only — stats strip + buckets `<details>` tables — as
an HTML fragment with no `<html>`/`<head>` chrome. `Content-Type:
text/html`. Byte-shape-identical to the same region in `GET /`, since
both execute the one shared template block.

### SSE frame (`GET /events`)

```
id: 42
data: {"seq":42,"time":"2026-07-07T12:00:00Z","method":"GET","bucket":"assets","key":"logo.png","status":200,"bytes":1024,"outcome":"fallback"}

```

### Buckets region (inside `/` and `/fragment`)

Per bucket: a `<details>` whose `<summary>` shows the bucket name,
object count, and total size, and whose body is a `<table>` of
`ObjectInfo` rows (key, size, content-type, ACL, created-at). A bucket
with zero objects renders an empty-state line instead of a table. All
object data is rendered inline at request time — there is no per-bucket
endpoint and no unknown-bucket 404 to handle (the page only ever lists
buckets that exist).

## File changes summary

- **Create** `traffic.go` — `TrafficEvent`, `TrafficBroker` (ring
  buffer, subscribers, counters), `WithTrafficCapture`,
  `splitBucketKey`, `classifyOutcome`.
- **Create** `admin.go` — `AdminServer`, route mux, single-page handler,
  `/fragment` handler, SSE handler, `writeSSE`, CSS route, view-model
  builder, template/asset embeds.
- **Create** `admin.html.tmpl` — one semantic-HTML template (no `class`
  attributes): the full page plus a `{{define "content"}}` block reused
  by `/fragment`; includes the inline feed + soft-refresh `<script>`.
  Embedded via `//go:embed`.
- **Vendor** `pico.classless.min.css` — Pico.css's classless build,
  committed at repo root, embedded and served at
  `/assets/pico.classless.min.css`. Retain its MIT license header
  comment in the file; note the third-party asset + license alongside
  the existing placeholder-image attribution.
- **Create** `traffic_test.go` — broker and classifier unit tests.
- **Create** `admin_test.go` — admin handler integration tests.
- **Modify** `debug.go` — rename `debugResponseWriter` →
  `countingResponseWriter` (and `newDebugResponseWriter` →
  `newCountingResponseWriter`); behavior unchanged; shared by both
  middlewares.
- **Modify** `storage.go` — `BucketInfo`, `ObjectInfo`, `ListBuckets`,
  `ListObjects`. Imports may gain nothing new (`os`, `filepath`,
  `time`, `sort` already used across the file/package as needed —
  verify per-file).
- **Modify** `handler.go` — set `X-Essie3-Fallback` on both fallback
  branches; add it to the CORS expose list.
- **Modify** `main.go` — start time, conditional broker + capture wrap,
  conditional loopback admin server, shutdown wiring, banner line.
- **Modify** `storage_test.go` — `ListBuckets` / `ListObjects` tests.
- **Modify** `handler_test.go` — assert `X-Essie3-Fallback` present on
  fallback GET/HEAD, absent on real-object and NoSuchKey responses.
- **Modify** `README.md` — an "Admin dashboard" subsection documenting
  `ESSIE3_ADMIN_PORT` and the single-page dashboard.
- **Modify** `Dockerfile` — `COPY` the embedded assets
  (`admin.html.tmpl`, `pico.classless.min.css`) before `go build`, since
  it copies sources selectively rather than the whole tree.
- **Modify** `debug_test.go` — update the type name if it references
  `debugResponseWriter` directly.

## Testing

### `traffic_test.go`

- **Ring eviction.** Publish `cap+N` events; `Subscribe`'s backlog
  returns exactly the last `cap`, oldest-first, with monotonic `Seq`.
- **Live fan-out.** Subscribe, publish, receive the event on the
  channel; the returned `cancel` removes the subscriber (a later
  publish does not block and the channel gets no more events).
- **Slow subscriber doesn't block.** A subscriber that never drains
  its channel must not stall `Publish` (fill the buffer, assert
  `Publish` still returns; the event is dropped for that subscriber
  only).
- **Counters.** A sequence of mixed outcomes yields the expected
  `reads` / `fallbacks`, and hit rate is `fallbacks/reads` (and 0 when
  `reads==0`).
- **`classifyOutcome`** — table-driven over every branch: fallback
  header set (pool and generate), 403, GET 404, GET 200/206/304, PUT
  200, POST 204, DELETE 204, and an "other" case.
- **`splitBucketKey`** — `/b` → `("b","")`, `/b/k` → `("b","k")`,
  `/b/a/c` → `("b","a/c")`, `/` → `("","")`.

### `storage_test.go` additions

- `ListBuckets` on a seeded dir returns buckets sorted, with correct
  counts and summed sizes; sidecars are not counted as objects.
- `ListObjects` reconstructs nested keys with `/`, excludes
  `.meta.json`, sorts, and pulls content-type/ACL/size from meta.
- An object with a **missing** sidecar is still listed, size falls back
  to on-disk body length, `CreatedAt` is zero.
- `ListObjects` on an unknown bucket returns `os.ErrNotExist`; on an
  invalid name returns `errInvalidName`.
- Missing `dataDir` → `ListBuckets` returns empty, nil error.

### `admin_test.go` (via `httptest.NewServer(admin.Handler())`)

- `GET /` → 200, `text/html`, contains the stats strip, the seeded
  bucket names/counts, the seeded object keys (inside their
  `<details>`), and the `EventSource('/events')` bootstrap.
- **Escaping.** A seeded key containing `<`/`&` is HTML-escaped in the
  `/` body (inject a key like `a<b` and assert it is escaped).
- `GET /fragment` → 200, `text/html`, contains the stats strip and
  bucket/object rows but **not** the `<html>`/`<head>` chrome or the
  feed `<script>` — proving it is the `content` block only.
- **Fragment ≡ page region.** The bytes of `/fragment` appear verbatim
  inside the `/` response (same shared block, no drift).
- `GET /events` with a pre-seeded broker → the response body contains
  the backlog frames (`id:` / `data:` JSON) for the seeded events.
  Drive it with a client that reads a bounded prefix then cancels the
  request context so the streaming handler returns.
- `GET /assets/pico.classless.min.css` → 200, `text/css`, non-empty.
- Unknown path (e.g. `GET /nope`) → 404. Non-GET on any admin route →
  405.

### `handler_test.go` additions

- Fallback GET on a missing key → response has `X-Essie3-Fallback`
  (`pool` in the default pool mode; `generate` under a generate-mode
  fixture), and it appears in `Access-Control-Expose-Headers`.
- Fallback HEAD → same header present.
- Real-object GET and a `NoSuchKey` GET → `X-Essie3-Fallback` absent.

CI stays green with the existing suite; the rename in `debug.go` is
mechanical and covered by `debug_test.go`.

## Out of scope

- **Any mutation** — upload, delete, bucket create, or ACL edits from
  the UI. Every route is `GET` by design. A future "manage" mode would
  be a separate spec and would need auth on the admin port first.
- **Object viewing/download via the dashboard** — list metadata only;
  fetch bytes from the S3 URL. Easy to add later behind a link, but out
  now.
- **Auth / non-loopback binding for the admin port** — loopback is the
  v1 containment. Exposing it beyond localhost requires an auth story.
- **Persisting traffic across restarts** — the ring buffer is
  in-memory and volatile. No on-disk event log.
- **Configurable ring size / poll interval via env** — `trafficRingSize`
  is a constant (500) for v1; promote to an env var only if a need
  surfaces, matching how the generated-fallback constants are handled.
- **Pagination / search on the object list** — a flat sorted table is
  fine at dev scale; revisit if buckets routinely hold thousands of
  keys.
- **A real S3 `ListObjectsV2` API** — this dashboard is a human UI, not
  the S3 XML listing operation, which remains explicitly unimplemented
  per the project's scope.
