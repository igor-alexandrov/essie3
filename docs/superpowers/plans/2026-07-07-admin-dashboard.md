# Admin Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only web dashboard on a separate loopback port (`ESSIE3_ADMIN_PORT`, opt-in) that lists buckets/objects, streams live request traffic via SSE, and shows rollup stats. Strictly observational — every route is `GET`. The S3 handler on `:9000` is unchanged except for one added response header.

**Architecture:** A new `traffic.go` provides an in-memory `TrafficBroker` (ring buffer + live subscribers + hit-rate counters) and a `WithTrafficCapture` middleware that publishes one `TrafficEvent` per request — reusing the status/bytes-capturing response writer from `debug.go` (renamed to a shared `countingResponseWriter`). A new `admin.go` serves a single semantic-HTML page (stats + live feed + `<details>` bucket/object listing) plus an SSE endpoint, a `/fragment` endpoint for soft-refresh, and the embedded stylesheet, styled by a vendored classless Pico.css build. `storage.go` gains read-only `ListBuckets`/`ListObjects`. `handler.go` sets `X-Essie3-Fallback` on its two fallback branches so the middleware can classify real-vs-fallback responses. `main.go` conditionally starts the loopback admin server when `ESSIE3_ADMIN_PORT` is set.

**Tech Stack:** Go, standard library only (`net/http`, `html/template`, `embed`, `encoding/json`, `sync`, `time`, `os`, `path/filepath`, `sort`). Styling is a vendored static asset (`pico.classless.min.css`) — no Go dependency, `go mod tidy` unaffected.

**Spec:** [`docs/superpowers/specs/2026-07-07-admin-dashboard-design.md`](../specs/2026-07-07-admin-dashboard-design.md)

---

## File Structure

- **Modify** `debug.go` — rename `debugResponseWriter` → `countingResponseWriter` and `newDebugResponseWriter` → `newCountingResponseWriter`; behavior byte-for-byte unchanged. Shared by both middlewares.
- **Modify** `debug_test.go` — update any direct references to the renamed type/constructor.
- **Create** `traffic.go` — `TrafficEvent`, `TrafficBroker` (ring buffer, subscribers, counters), `WithTrafficCapture`, `splitBucketKey`, `classifyOutcome`.
- **Create** `traffic_test.go` — broker and classifier unit tests.
- **Modify** `storage.go` — `BucketInfo`, `ObjectInfo`, `ListBuckets`, `ListObjects` (read-only, no locks, sidecar-aware).
- **Modify** `storage_test.go` — `ListBuckets`/`ListObjects` tests.
- **Modify** `handler.go` — set `X-Essie3-Fallback` on both fallback branches; add it to the CORS expose list.
- **Modify** `handler_test.go` — assert `X-Essie3-Fallback` present on fallback GET/HEAD, absent on real-object and NoSuchKey responses.
- **Vendor** `pico.classless.min.css` — Pico.css classless build at repo root, embedded and served at `/assets/pico.classless.min.css`; retain its MIT license header.
- **Create** `admin.html.tmpl` — one semantic-HTML template (no `class` attributes): full page + a `{{define "content"}}` block reused by `/fragment`, plus the inline feed + soft-refresh `<script>`. Embedded via `//go:embed`.
- **Create** `admin.go` — `AdminServer`, route mux, single-page handler, `/fragment` handler, SSE handler, `writeSSE`, CSS route, shared view-model builder, template/asset embeds.
- **Create** `admin_test.go` — admin handler integration tests.
- **Modify** `main.go` — start time, conditional broker + capture wrap, conditional loopback admin server, shutdown wiring, banner line.
- **Modify** `README.md` — "Admin dashboard" subsection + env var row + third-party asset attribution.

---

### Task 1: Rename the shared response writer (refactor, no behavior change)

Promote `debug.go`'s status/bytes-capturing writer to a neutral name so both middlewares can share it. Pure rename — this must not change any behavior; the existing debug tests are the safety net.

**Files:**
- Modify: `debug.go`
- Modify: `debug_test.go`

- [ ] **Step 1: Rename in `debug.go`**

`debugResponseWriter` → `countingResponseWriter`; `newDebugResponseWriter` → `newCountingResponseWriter`. Update the doc comments to drop "debug"-specific wording (it is now shared). Leave `WithDebugLogging` and everything else untouched.

- [ ] **Step 2: Update `debug_test.go`**

Rename any direct references. If the tests only exercise `WithDebugLogging` (black-box), there may be nothing to change.

- [ ] **Step 3: Verify tests pass**

```sh
cd /home/igor/Work/essie3 && go test -run TestDebug -v ./... && go vet ./...
```
Expected: PASS; no behavior change.

- [ ] **Step 4: Commit**

```sh
git add debug.go debug_test.go
git commit -m "Rename debugResponseWriter to shared countingResponseWriter"
```

---

### Task 2: Traffic broker + capture middleware (TDD)

The in-memory event model, ring buffer, live fan-out, counters, and the wrapping middleware. No admin server yet — this task is purely the pub/sub core.

**Files:**
- Create: `traffic.go`
- Create: `traffic_test.go`

- [ ] **Step 1: Write the failing tests** (`traffic_test.go`)

Cover (see spec §Testing / `traffic_test.go`):
- Ring eviction: publish `cap+N`, `Subscribe` backlog returns exactly the last `cap`, oldest-first, monotonic `Seq`.
- Live fan-out: subscribe → publish → receive on channel; `cancel` removes the subscriber.
- Slow subscriber does not block `Publish` (fill the buffered channel; `Publish` still returns; event dropped for that subscriber only).
- Counters: mixed outcomes yield expected `reads`/`fallbacks`; hit rate is `fallbacks/reads`, and 0 when `reads==0`.
- `classifyOutcome` — table-driven over every branch: fallback header `pool` and `generate`, 403 → `denied`, GET 404 → `miss`, GET 200/206/304 → `real`, PUT 200 → `write`, POST 204 → `write`, DELETE 204 → `delete`, plus an "other" fallthrough.
- `splitBucketKey`: `/b`→`("b","")`, `/b/k`→`("b","k")`, `/b/a/c`→`("b","a/c")`, `/`→`("","")`.

- [ ] **Step 2: Verify the tests fail (compile error)**

```sh
cd /home/igor/Work/essie3 && go test -run "TestTraffic|TestClassifyOutcome|TestSplitBucketKey" -v ./...
```
Expected: `undefined: NewTrafficBroker`, `undefined: classifyOutcome`, etc.

- [ ] **Step 3: Implement `traffic.go`**

Per spec §Architecture / `traffic.go`:
- `TrafficEvent` struct (`Seq`, `Time`, `Method`, `Bucket`, `Key`, `Status`, `Bytes`, `Outcome`).
- `TrafficBroker` with `mu sync.Mutex`, ring slice + `cap`, `seq`, `subs map[chan TrafficEvent]struct{}`, and `reads`/`fallbacks` counters.
- `NewTrafficBroker(capacity int) *TrafficBroker`.
- `Publish(e TrafficEvent)` — assign `Seq`, append with oldest-eviction, bump counters from the event's outcome (`miss`/`fallback` → `reads++`; `fallback` → `fallbacks++`), non-blocking send to each subscriber (`select { case ch <- e: default: }`).
- `Subscribe() (backlog []TrafficEvent, ch chan TrafficEvent, cancel func())` — snapshot the ring, register a **buffered** channel, return a `cancel` that unregisters and closes.
- `Stats() (reads, fallbacks uint64)`.
- `WithTrafficCapture(next http.Handler, broker *TrafficBroker) http.Handler` — wrap with `newCountingResponseWriter`, call `next`, then `broker.Publish(...)` reading `crw.status`, `crw.bytes`, and `crw.Header().Get("X-Essie3-Fallback")`.
- `splitBucketKey(path string) (bucket, key string)` — mirror `ServeHTTP`'s `TrimPrefix("/")` + `SplitN(path, "/", 2)` split.
- `classifyOutcome(method string, status int, fallback string) string` — per the mapping above.

- [ ] **Step 4: Verify the tests pass, race-clean**

```sh
cd /home/igor/Work/essie3 && go test -race -run "TestTraffic|TestClassifyOutcome|TestSplitBucketKey" -v ./...
```
Expected: PASS with `-race` (the broker is concurrent — publish vs subscribe vs cancel).

- [ ] **Step 5: Commit**

```sh
git add traffic.go traffic_test.go
git commit -m "Add traffic broker and capture middleware"
```

---

### Task 3: Read-only storage listing (TDD)

`ListBuckets`/`ListObjects` — filesystem walks with no per-key locks, sidecar-aware, tolerant of missing/unparseable meta. Independent of the traffic core.

**Files:**
- Modify: `storage.go`
- Modify: `storage_test.go`

- [ ] **Step 1: Write the failing tests** (`storage_test.go`, appended)

Cover (see spec §Testing / `storage_test.go`):
- `ListBuckets` on a seeded dir → buckets sorted by name, correct object counts and summed sizes; sidecars not counted as objects.
- `ListObjects` reconstructs nested keys with `/`, excludes `.meta.json`, sorts, and pulls content-type/ACL/size from meta.
- Object with a **missing** sidecar is still listed; `Size` falls back to on-disk body length; `CreatedAt` is zero.
- `ListObjects` on an unknown bucket → `os.ErrNotExist`; on an invalid name → `errInvalidName`.
- Missing `dataDir` → `ListBuckets` returns empty slice, nil error.

Seed fixtures by writing through the existing `Storage.PutObject` where possible (keeps meta realistic), and by hand-writing a body-without-sidecar for the missing-meta case.

- [ ] **Step 2: Verify the tests fail (compile error)**

```sh
cd /home/igor/Work/essie3 && go test -run "TestListBuckets|TestListObjects" -v ./...
```
Expected: `undefined: (*Storage).ListBuckets`, etc.

- [ ] **Step 3: Implement in `storage.go`**

Per spec §Architecture / `storage.go`:
- Add `BucketInfo` and `ObjectInfo` structs.
- `ListBuckets()` — read `dataDir` entries; for each directory, count non-sidecar files and sum sizes (via `ListObjects` or a shared walk helper); sort by `Name`; missing `dataDir` → `([]BucketInfo{}, nil)`.
- `ListObjects(bucket)` — `validateName(bucket)` first; `filepath.WalkDir` under `data/<bucket>`; skip entries ending in `.meta.json`; for each object, build the `/`-joined key relative to the bucket root, read the sidecar via `readMeta` (best-effort — on error, `Size` from `os.Stat`, other fields zero); sort by `Key`. Unknown bucket → wrap `os.ErrNotExist`.
- No locks; document why in a comment (best-effort dashboard; must never block the write path).

- [ ] **Step 4: Verify the tests pass, race-clean**

```sh
cd /home/igor/Work/essie3 && go test -race -run "TestListBuckets|TestListObjects" -v ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add storage.go storage_test.go
git commit -m "Add read-only ListBuckets and ListObjects"
```

---

### Task 4: `X-Essie3-Fallback` response header (TDD)

The one S3-handler change: emit the header the traffic middleware classifies on, plus expose it via CORS.

**Files:**
- Modify: `handler.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing tests** (`handler_test.go`, appended)

Cover (see spec §Testing / `handler_test.go`):
- Fallback GET on a missing key → response has `X-Essie3-Fallback: pool` (default pool mode), and the header name appears in `Access-Control-Expose-Headers`.
- Fallback GET under a `generate`-mode fixture → `X-Essie3-Fallback: generate`.
- Fallback HEAD → header present.
- Real-object GET and a `NoSuchKey` GET → `X-Essie3-Fallback` **absent**.

Reuse the existing `testServer(t)` / mode-specific fixtures already present from the generated-fallback work.

- [ ] **Step 2: Verify the tests fail**

```sh
cd /home/igor/Work/essie3 && go test -run "TestHandler.*Fallback.*Header|TestHandler.*XEssie3" -v ./...
```
Expected: header assertions fail (not currently set).

- [ ] **Step 3: Modify `handler.go`**

In the fallback block of both `handleGetObject` ([handler.go:180]) and `handleHeadObject` ([handler.go:265]), after the existing `Content-Type` line:

```go
if p.Generated {
    w.Header().Set("X-Essie3-Fallback", "generate")
} else {
    w.Header().Set("X-Essie3-Fallback", "pool")
}
```

In `setCORS`, add `X-Essie3-Fallback` to the `Access-Control-Expose-Headers` value.

- [ ] **Step 4: Verify the tests pass**

```sh
cd /home/igor/Work/essie3 && go test -run "TestHandler" -v ./...
```
Expected: PASS — real-object/error responses are unchanged (header only ever set on fallback branches).

- [ ] **Step 5: Commit**

```sh
git add handler.go handler_test.go
git commit -m "Set X-Essie3-Fallback header on fallback responses"
```

---

### Task 5: Vendor Pico.css and the single-page template

Add the static assets Task 6 embeds. No Go code yet — just the files.

**Files:**
- Vendor: `pico.classless.min.css`
- Create: `admin.html.tmpl`

- [ ] **Step 1: Vendor the stylesheet (pinned version)**

Download the classless build of a pinned Pico release and commit it verbatim, keeping its license header:

```sh
cd /home/igor/Work/essie3 && \
  curl -fsSL https://cdn.jsdelivr.net/npm/@picocss/pico@2.0.6/css/pico.classless.min.css \
    -o pico.classless.min.css
```

(If the executor is offline, fetch the same file via WebFetch or copy from a local Pico install. Do **not** hand-edit it — treat it as a vendored artifact.)

- [ ] **Step 2: Write `admin.html.tmpl`**

One file, semantic HTML only — no `class` attributes (the classless build styles bare elements). Structure:

- **Full page** (the default/root template): `<head>` links `<link rel="stylesheet" href="/assets/pico.classless.min.css">` and sets `<title>`; `<body>` has a `<header>` (title only — there are no sub-pages to navigate to), then `{{template "content" .}}`, then the live feed: an empty `<table id="feed">` with a `<thead>` (time, method, key, status, outcome), then the inline `<script>`.
- **`{{define "content"}}` block** — the soft-refreshable region wrapped in a container element (e.g. `<section id="content">`): the stats strip (uptime, bucket count, total objects, total bytes, fallback hit rate) and, per bucket, a `<details>` whose `<summary>` shows name + object count + total size and whose body is a `<table>` of objects (key, size, content-type, ACL, created-at). A zero-object bucket renders an empty-state line, not a table.
- **`<script>`** (a couple dozen lines of vanilla JS): open `new EventSource('/events')`; on each message parse the JSON `data:`, prepend a `<tr>` to the feed, trim to ~200 rows; if `outcome` is `write` or `delete`, restart a ~750 ms debounce timer that on fire does `fetch('/fragment')` and replaces `#content`'s `innerHTML` with the response text.

Escaping is automatic (`html/template`); no manual escaping needed. The `content` block is what `/fragment` renders alone, so keep it self-contained (no dependency on page-level chrome).

- [ ] **Step 3: Sanity-check the assets exist and are non-empty**

```sh
cd /home/igor/Work/essie3 && wc -c pico.classless.min.css admin.html.tmpl
```
Expected: the CSS is tens of KB; the template is non-empty.

- [ ] **Step 4: Commit**

```sh
git add pico.classless.min.css admin.html.tmpl
git commit -m "Vendor Pico.css and add single-page admin template"
```

---

### Task 6: Admin server, routes, SSE, and fragment (TDD)

Wire the embedded assets, `TrafficBroker`, and storage listing into a read-only single-page `http.Handler` with a soft-refresh fragment endpoint.

**Files:**
- Create: `admin.go`
- Create: `admin_test.go`

- [ ] **Step 1: Write the failing tests** (`admin_test.go`)

Drive via `httptest.NewServer((&AdminServer{...}).Handler())` with a pre-seeded `Storage` and `TrafficBroker` (see spec §Testing / `admin_test.go`):
- `GET /` → 200, `text/html`, body contains the stats strip, seeded bucket names/counts, seeded object keys (inside `<details>`), and `EventSource('/events')`.
- A seeded key containing `<`/`&` is HTML-escaped in the `/` body.
- `GET /fragment` → 200, `text/html`, contains the stats strip and bucket/object rows but **not** the `<html>`/`<head>` chrome or the feed `<script>`.
- **Fragment ≡ page region:** the `/fragment` body appears verbatim inside the `/` body (same shared `content` block).
- `GET /events` with a broker pre-seeded with events → body contains the backlog `id:`/`data:` frames. Drive with a client that reads a bounded prefix then cancels the request context so the streaming handler returns (don't read forever).
- `GET /assets/pico.classless.min.css` → 200, `text/css`, non-empty.
- Unknown path → 404. Non-GET on any admin route → 405.

- [ ] **Step 2: Verify the tests fail (compile error)**

```sh
cd /home/igor/Work/essie3 && go test -run "TestAdmin" -v ./...
```
Expected: `undefined: AdminServer`.

- [ ] **Step 3: Implement `admin.go`**

Per spec §Architecture / `admin.go`:
- `//go:embed admin.html.tmpl` into a `string` and `pico.classless.min.css` into a `[]byte`.
- `AdminServer{storage, fallback, broker, startedAt, tmpl}`.
- `NewAdminServer(...)` — parse the one template once (`template.New("page").Parse(adminTemplate)`), which registers both the root page and the `content` block.
- A shared `viewModel()` helper computing stats (`broker.Stats()` → hit rate, `time.Since(startedAt)` uptime) + `ListBuckets` and per-bucket `ListObjects`, used by both page handlers.
- `Handler()` returns a `*http.ServeMux` with the spec's routes (`/`, `/fragment`, `/events`, `/assets/pico.classless.min.css`, `/healthz`); reject non-GET with 405; unknown paths 404. (Note: `/` on a `ServeMux` is the catch-all — the root handler must return 404 for any path that isn't exactly `/`.)
- Root handler executes the whole template; `/fragment` executes only the `content` block (`tmpl.ExecuteTemplate(w, "content", vm)`) with `Content-Type: text/html`.
- `handleEvents` — SSE per the spec: set event-stream headers, `Subscribe`, replay backlog, then `select` on the channel vs `r.Context().Done()`; `defer cancel()`.
- `writeSSE(w, e)` — emit `id: <Seq>\n` + `data: <json>\n\n` and rely on the caller to `Flush`.
- CSS handler — serve `adminCSS` with `Content-Type: text/css` and a long `Cache-Control`.

- [ ] **Step 4: Verify the tests pass, race-clean**

```sh
cd /home/igor/Work/essie3 && go test -race -run "TestAdmin" -v ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add admin.go admin_test.go
git commit -m "Add read-only single-page admin dashboard with SSE feed and soft-refresh fragment"
```

---

### Task 7: Wire the admin server into `main.go`

Opt-in start on `ESSIE3_ADMIN_PORT`, loopback-bound, with capture-wrap and graceful shutdown.

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Implement**

Per spec §Architecture / `main.go`:
- Capture `startedAt := time.Now()` early.
- When `ESSIE3_ADMIN_PORT` is set: `broker := NewTrafficBroker(trafficRingSize)` (const `500`), and wrap the base S3 handler with `WithTrafficCapture(handler, broker)` **before** the optional debug wrap.
- Build `adminSrv := &http.Server{Addr: "127.0.0.1:"+port, Handler: NewAdminServer(storage, fallback, broker, startedAt).Handler(), ReadHeaderTimeout: 10*time.Second}` — **no `WriteTimeout`** (it would sever SSE).
- Start it in a goroutine (log non-`ErrServerClosed` errors).
- Add `adminSrv.Shutdown(shutdownCtx)` to the existing shutdown block.
- Add a banner line `  admin:    http://127.0.0.1:<port>` when enabled.

- [ ] **Step 2: Verify it builds and vets**

```sh
cd /home/igor/Work/essie3 && go build ./... && go vet ./...
```
Expected: PASS.

- [ ] **Step 3: Smoke test end-to-end**

```sh
cd /home/igor/Work/essie3 && ESSIE3_ADMIN_PORT=9001 go run . &
sleep 1
curl -s -X PUT http://localhost:9000/demo >/dev/null
curl -s -X PUT --data-binary 'hi' http://localhost:9000/demo/a.txt >/dev/null
curl -s http://localhost:9000/demo/a.txt >/dev/null           # real read
curl -s http://localhost:9000/demo/missing.png >/dev/null      # fallback read
curl -s http://localhost:9001/ | grep -q demo && echo "PAGE_LISTS_BUCKET"
curl -s http://localhost:9001/ | grep -q 'EventSource' && echo "DASH_OK"
curl -s http://localhost:9001/fragment | grep -q demo && echo "FRAGMENT_OK"
curl -s http://localhost:9001/assets/pico.classless.min.css | head -c 20
pkill -f 'go run .'
```
Expected: `PAGE_LISTS_BUCKET`, `DASH_OK`, `FRAGMENT_OK`, and CSS bytes; the default (no `ESSIE3_ADMIN_PORT`) run must **not** open :9001.

- [ ] **Step 4: Commit**

```sh
git add main.go
git commit -m "Start loopback admin dashboard when ESSIE3_ADMIN_PORT is set"
```

---

### Task 8: Document in `README.md`

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Env var + section**

- Add an `ESSIE3_ADMIN_PORT` row to the Configuration table (opt-in; loopback-only; default disabled).
- Add an "Admin dashboard" subsection: the single-page layout (stats, live feed, bucket/object listing), the live SSE feed + soft auto-refresh, that it is read-only (no upload/delete/config), and that it binds `127.0.0.1` only because it is unauthenticated.
- Add a one-line third-party attribution for the vendored Pico.css (MIT), alongside the existing placeholder-image credit.

- [ ] **Step 2: Commit**

```sh
git add README.md
git commit -m "Document admin dashboard and ESSIE3_ADMIN_PORT"
```

---

### Task 9: Full CI-equivalent verification

- [ ] **Step 1: Run the full local CI sequence**

```sh
cd /home/igor/Work/essie3 && \
  go build ./... && \
  go vet ./... && \
  gofmt -l . && \
  go test -race -count=1 ./... && \
  go mod tidy && git diff --exit-code go.mod $(test -f go.sum && echo go.sum)
```

All must pass:
- `go build` / `go vet` — clean (embed directives resolve; assets present).
- `gofmt -l .` — empty output.
- `go test -race -count=1` — green, including the concurrent broker tests.
- `go mod tidy` — no diff (Pico.css is a static asset, not a Go dep; stdlib-only preserved).

- [ ] **Step 2: Confirm the Docker build still embeds assets**

```sh
cd /home/igor/Work/essie3 && docker build -t essie3-admin-check . && echo "DOCKER_OK"
```
Expected: build succeeds — `*.tmpl`/`*.css` are not `.dockerignore`d, so `go:embed` finds them. (Skip only if Docker is unavailable in the environment; note it if skipped.)

---

## Self-Review Notes

**Spec coverage:**
- Shared `countingResponseWriter` (rename) → Task 1.
- `TrafficEvent`, `TrafficBroker` (ring, subscribers, counters), `WithTrafficCapture`, `splitBucketKey`, `classifyOutcome` → Task 2.
- Non-blocking fan-out / slow-subscriber safety → Task 2 (Step 1 test + `select…default` in Publish).
- Read-only `ListBuckets`/`ListObjects`, sidecar-aware, missing-meta tolerant, no locks → Task 3.
- `X-Essie3-Fallback` on both fallback branches + CORS expose → Task 4.
- Vendored classless Pico.css + `//go:embed` + one semantic template (no `class`) → Tasks 5, 6.
- Single page (`/`) with stats + live feed + `<details>` buckets; `/fragment` renders the shared `content` block for soft-refresh; `/events` SSE (backlog + live) + CSS route + `/healthz`; non-GET → 405; unknown → 404 → Task 6.
- Soft auto-refresh: SSE feed live; write/delete events trigger a ~750 ms debounced `fetch('/fragment')` that swaps the stats+listing region → Tasks 5 (script) + 6 (fragment endpoint).
- Opt-in loopback admin server, no `WriteTimeout`, capture-wrap, graceful shutdown, banner line → Task 7.
- `ESSIE3_ADMIN_PORT` unset = disabled (zero behavior change) → Task 7 (Step 3 asserts default run does not open :9001).
- README docs + attribution → Task 8.
- Docker/embed constraint verified → Task 9 Step 2.

**Out of scope (confirmed not in plan):** any mutation (upload/delete/create/ACL), object viewing/download through the UI, auth or non-loopback binding on the admin port, persisting traffic across restarts, env-configurable ring size / poll interval, pagination/search on the object list, a real S3 `ListObjectsV2` API.
