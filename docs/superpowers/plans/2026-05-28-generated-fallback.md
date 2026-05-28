# Generated Fallback Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `generate` fallback mode that produces deterministic bubble-identicon PNG/JPEG images from `MD5(key)`, plus a `both` mode that tries the existing pool first. Selected via the new `ESSIE3_FALLBACK_MODE` env var; default `pool` preserves current behavior.

**Architecture:** A new `generate.go` provides `generateImage(key)` which renders a 512×512 bubble identicon (16 translucent overlapping circles, dakridge layout) and encodes PNG or JPEG based on the key's extension. `fallback.go` gains a `FallbackMode` enum, mode-aware `Select`, and `ETag`/`Generated` fields on `Placeholder`. `handler.go`'s existing fallback branches gain three lines to expose the generated `ETag` / `Last-Modified` headers and thread the ETag into `evaluateRange`.

**Tech Stack:** Go, standard library only (`image`, `image/color`, `image/draw`, `image/png`, `image/jpeg`, `crypto/md5`, `encoding/hex`, `time`).

**Spec:** [`docs/superpowers/specs/2026-05-27-generated-fallback-design.md`](../specs/2026-05-27-generated-fallback-design.md)

---

## File Structure

- **Create** `generate.go` — bubble layout derivation, anti-aliased circle mask, identicon rendering, PNG/JPEG encoding, hardcoded `category20` palette.
- **Create** `generate_test.go` — unit tests for `generateImage`, `bubbleFromDigest`, `circleMask`.
- **Modify** `fallback.go` — `FallbackMode` type + `ParseFallbackMode`, mode/generatedAt fields on `Fallback`, `ETag`/`Generated` fields on `Placeholder`, mode-aware `Select` + `selectFromPool` + `generate`, `LastModified` accessor, `NewFallback` signature gains `mode FallbackMode`.
- **Modify** `fallback_test.go` — append mode-behavior tests; update existing `NewFallback` call sites to pass `FallbackModePool`.
- **Modify** `main.go` — read `ESSIE3_FALLBACK_MODE`, parse via `ParseFallbackMode` (fatal on invalid), pass to `NewFallback`, print resolved mode in startup banner.
- **Modify** `handler.go` — in both fallback branches of `handleGetObject` and `handleHeadObject`: set `ETag` from `p.ETag` if non-empty, set `Last-Modified` if `p.Generated`, pass `p.ETag` into `evaluateRange`. Update the single `NewFallback` call in `handler_test.go`'s helper.
- **Modify** `handler_test.go` — integration tests for `mode=generate` (GET/HEAD/Range/If-None-Match/extension-rejection) and `mode=both` (pool-first + generate-fallback).
- **Modify** `README.md` — short subsection under Fallback documenting the new env var and the three modes.

---

### Task 1: Bubble identicon generator (TDD)

Pure-function `generateImage` and its helpers. No coupling to `Fallback` yet — this task is purely about correct image generation.

**Files:**
- Create: `generate.go`
- Create: `generate_test.go`

- [ ] **Step 1: Write the failing tests** (`generate_test.go`)

Cover: extension routing (`generateImage` returns non-nil for `.png`/`.jpg`/`.jpeg`, nil for others); determinism; distinctness; PNG and JPEG decode to 512×512; `bubbleFromDigest` table-driven against hand-computed expectations; `circleMask` center=255, far-corner=0, boundary intermediate; the rendered canvas is not uniformly white. See spec §Testing for the full list.

- [ ] **Step 2: Verify the tests fail (compile error)**

```sh
cd /Users/igor/workspace/essie3 && go test -run TestGenerate -v ./...
```
Expected: `undefined: generateImage`.

- [ ] **Step 3: Implement `generate.go`**

Per spec §Architecture and §Generation algorithm. Key constants: `canvasSize = 512`, `gridDivisions = 16`, `minRadius = 6.0`, `maxRadius = 256.0`, `bubbleAlpha = 0.75`, `bubbleCount = 16`, `category20` palette. Anti-aliased mask via 4×4 subsampling; `draw.DrawMask` with `draw.Over`. ETag is *not* computed here — `generateImage` returns only `(body, contentType)`; the caller computes the ETag.

- [ ] **Step 4: Verify the tests pass**

```sh
cd /Users/igor/workspace/essie3 && go test -run TestGenerate -v ./...
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add generate.go generate_test.go
git commit -m "Add bubble identicon generator for fallback mode"
```

---

### Task 2: `FallbackMode` and mode-aware `Select` (TDD)

Add the enum and mode plumbing on `Fallback`. The existing pool tests must keep passing — all of them get `FallbackModePool` appended.

**Files:**
- Modify: `fallback.go`
- Modify: `fallback_test.go`
- Modify: `handler_test.go` (one existing `NewFallback` call site)
- Modify: `main.go` (one existing `NewFallback` call site — temporarily pass `FallbackModePool` until Task 4 wires the env var)

- [ ] **Step 1: Write the failing tests** (`fallback_test.go`, appended)

Cover: `ParseFallbackMode` valid/empty/invalid; `Select` under each mode for matching/non-matching extensions; the ETag on a generated `Placeholder` matches `"<hex>"` where `<hex> = md5(Body)`.

- [ ] **Step 2: Verify the tests fail (compile error)**

```sh
cd /Users/igor/workspace/essie3 && go test -run "TestParseFallbackMode|TestFallbackSelect_Mode|TestFallbackGenerate_ETag" -v ./...
```
Expected: `undefined: FallbackMode`, `undefined: ParseFallbackMode`, etc.

- [ ] **Step 3: Modify `fallback.go`**

- Add `FallbackMode` enum with `FallbackModePool` (iota = 0), `FallbackModeGenerate`, `FallbackModeBoth`.
- Add `ParseFallbackMode(s string) (FallbackMode, error)` — empty → pool, valid string → matching constant, anything else → descriptive error.
- Add `mode FallbackMode` and `generatedAt time.Time` fields on `Fallback`.
- Add `ETag string` and `Generated bool` fields on `Placeholder`.
- Change `NewFallback(dir string, inlineExts []string, mode FallbackMode)` — set `mode` and `generatedAt: time.Now().UTC()`.
- Rename the current `Select` body to `selectFromPool`.
- Add new `Select` that dispatches on mode: pool → `selectFromPool`; generate → `generate`; both → `selectFromPool` first, then `generate` if pool returns nil.
- Add `generate(key string) *Placeholder` — calls `generateImage(key)`; if non-nil, computes `md5.Sum(body)` and returns a `Placeholder{Body, ContentType, ETag: "\"" + hex.EncodeToString(sum[:]) + "\"", Generated: true}`.
- Add `LastModified() time.Time` returning `fb.generatedAt`.
- New imports: `crypto/md5`, `encoding/hex`, `time`.

- [ ] **Step 4: Update existing call sites to pass `FallbackModePool`**

`fallback_test.go` (~14 call sites), `handler_test.go` (1 call site in the test helper), `main.go` (1 call site — will be replaced in Task 4). Append `, FallbackModePool` to each.

- [ ] **Step 5: Verify all tests pass**

```sh
cd /Users/igor/workspace/essie3 && go test ./...
```
Expected: PASS. The pre-existing fallback tests should be unaffected (they all use `FallbackModePool`).

- [ ] **Step 6: Commit**

```sh
git add fallback.go fallback_test.go handler_test.go main.go
git commit -m "Add FallbackMode and mode-aware Select"
```

---

### Task 3: Wire `ESSIE3_FALLBACK_MODE` in `main.go`

Read the env var, parse it, pass to `NewFallback`, log the resolved mode.

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Implement**

Replace the `FallbackModePool` literal placeholder from Task 2 with:

```go
modeStr := os.Getenv("ESSIE3_FALLBACK_MODE")
mode, err := ParseFallbackMode(modeStr)
if err != nil {
    log.Fatalf("invalid ESSIE3_FALLBACK_MODE %q: %v", modeStr, err)
}
fallback, err := NewFallback(fallbackDataDir, inlineExts, mode)
```

Add a startup line after the existing `fmt.Printf("  fallback: ...")`:

```go
fmt.Printf("  fallback mode: %s\n", modeStr)  // empty prints as blank; use a helper if a default-label is wanted
```

Print `"pool (default)"` for the empty string for clarity; otherwise echo `modeStr`.

- [ ] **Step 2: Verify it still builds**

```sh
cd /Users/igor/workspace/essie3 && go build ./...
```
Expected: PASS.

- [ ] **Step 3: Smoke test startup**

```sh
cd /Users/igor/workspace/essie3 && go run . &  # default mode
sleep 1; pkill -f 'go run .'
ESSIE3_FALLBACK_MODE=both go run . &
sleep 1; pkill -f 'go run .'
ESSIE3_FALLBACK_MODE=bogus go run . 2>&1 | grep 'invalid ESSIE3_FALLBACK_MODE'
```
Expected: first two start cleanly, banner shows the resolved mode; third exits non-zero with the parse error.

- [ ] **Step 4: Commit**

```sh
git add main.go
git commit -m "Wire ESSIE3_FALLBACK_MODE into Fallback construction"
```

---

### Task 4: Handler exposes generated `ETag` / `Last-Modified` (TDD)

Three small edits per fallback branch (both in `handleGetObject` and `handleHeadObject`).

**Files:**
- Modify: `handler.go`
- Modify: `handler_test.go`

- [ ] **Step 1: Write the failing tests** (`handler_test.go`, appended)

Cover:
- `mode=generate`, GET missing `.png` → 200, `Content-Type: image/png`, body decodes as 512×512 PNG, `ETag` is `"32-hex"` quoted, `Last-Modified` is set, `Accept-Ranges: bytes`.
- `mode=generate`, GET missing `.jpg` → 200 + JPEG decode.
- `mode=generate`, GET missing `.pdf` → 404 NoSuchKey.
- `mode=generate`, HEAD missing `.png` → 200, same headers as GET, empty body.
- `mode=generate`, GET with `Range: bytes=0-99` → 206, `Content-Range: bytes 0-99/<total>`.
- `mode=generate`, GET with `If-None-Match: <ETag>` → 304 (this works automatically once `evaluateRange` sees the ETag — actually `If-None-Match` is unrelated to `evaluateRange`, just verify the ETag is sent so a client can use it). Skip this if conditional GET semantics aren't implemented in essie3 yet; instead verify the ETag header value is byte-identical across two requests.
- `mode=generate`, two GETs on the same `.png` key → byte-identical responses.
- `mode=both`, missing key with pool match (e.g., `.pdf` which is in testdata) → returns the pool placeholder bytes (not a generated one).
- `mode=both`, missing `.png` with no pool entry for `.png` → generated image.

Use a new helper `testServerWithFallback(t, fb)` (or inline construction) so tests can pick the mode. The existing `testServer(t)` keeps `FallbackModePool`.

- [ ] **Step 2: Verify the tests fail**

```sh
cd /Users/igor/workspace/essie3 && go test -run "TestHandler_Generate|TestHandler_Both" -v ./...
```
Expected: failures — generated responses currently lack `ETag` and `Last-Modified` (and `mode=generate` doesn't exist yet in test setup, but Task 2 made it accessible).

- [ ] **Step 3: Modify `handler.go`**

In both `handleGetObject` and `handleHeadObject`, inside the `if p := h.fallback.Select(key); p != nil { ... }` block:

1. Replace `evaluateRange(r, totalLen, "")` with `evaluateRange(r, totalLen, p.ETag)`.
2. If `p.ETag != ""`, `w.Header().Set("ETag", p.ETag)` after the `Accept-Ranges` line.
3. If `p.Generated`, `w.Header().Set("Last-Modified", h.fallback.LastModified().UTC().Format(http.TimeFormat))` after the ETag line.

- [ ] **Step 4: Verify the tests pass**

```sh
cd /Users/igor/workspace/essie3 && go test -run "TestHandler_Generate|TestHandler_Both" -v ./...
```
Expected: PASS.

- [ ] **Step 5: Verify the full suite passes**

```sh
cd /Users/igor/workspace/essie3 && go test ./...
```
Expected: PASS — existing fallback tests use `FallbackModePool` and their `Placeholder.ETag` is empty, so the new `if p.ETag != ""` guard preserves their response shape exactly.

- [ ] **Step 6: Commit**

```sh
git add handler.go handler_test.go
git commit -m "Expose generated ETag and Last-Modified on fallback responses"
```

---

### Task 5: Document `ESSIE3_FALLBACK_MODE` in `README.md`

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the env var table**

Add a row for `ESSIE3_FALLBACK_MODE` describing `pool` / `generate` / `both` and the default (`pool`).

- [ ] **Step 2: Add a subsection under the Fallback section**

A short paragraph: the three modes, what `generate` does (bubble identicons from `MD5(key)`, PNG and JPEG only), that `generate`/`both` produce stable `ETag` and `Last-Modified` headers so HTTP caching works, and a one-line note that non-PNG/JPEG extensions fall through to `NoSuchKey` under `generate`.

- [ ] **Step 3: Commit**

```sh
git add README.md
git commit -m "Document ESSIE3_FALLBACK_MODE and generate-mode behavior"
```

---

### Task 6: Full CI-equivalent verification

- [ ] **Step 1: Run the full local CI sequence**

```sh
cd /Users/igor/workspace/essie3 && \
  go vet ./... && \
  gofmt -l . && \
  go test -race -count=1 ./... && \
  go mod tidy && git diff --exit-code go.mod go.sum
```

All four must pass:
- `go vet` — clean.
- `gofmt -l .` — empty output.
- `go test -race -count=1` — green.
- `go mod tidy` — no diff (no new third-party deps; stdlib-only enforced).

---

## Self-Review Notes

**Spec coverage:**
- `generate.go` with `generateImage`, `bubbleFromDigest`, `circleMask`, `renderIdenticon`, `category20` → Task 1.
- Anti-aliased circle drawing via `*image.Alpha` mask + `draw.DrawMask` → Task 1.
- Dakridge bubble algorithm faithfully mirrored (16 bubbles, `(x*y) % 32` radius quirk, `(x+y) % 20` color index) → Task 1.
- `FallbackMode` enum + `ParseFallbackMode` → Task 2.
- Mode-aware `Select` with pool-first under `both` → Task 2.
- `Placeholder.ETag` and `Placeholder.Generated` → Task 2.
- `Fallback.LastModified()` returning process start time → Task 2.
- `ESSIE3_FALLBACK_MODE` env var + fatal-on-invalid + startup banner → Task 3.
- Default `pool` behavior preserved (zero behavior change for existing users) → Task 2 (existing tests pass after appending `FallbackModePool`) and Task 4 (existing fallback integration tests still green).
- Generated ETag + Last-Modified headers on fallback responses → Task 4.
- `p.ETag` threaded into `evaluateRange` so `If-Range` works against generated bytes → Task 4.
- README documentation for the new env var → Task 5.

**Out of scope (confirmed not in plan):** caching generated bytes, env-configurable dimensions/bubble count/alpha/JPEG quality, formats beyond PNG/JPEG, text overlay, backfilling ETag/Last-Modified for pool placeholders, per-bucket mode overrides.
