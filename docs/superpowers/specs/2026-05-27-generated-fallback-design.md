# Generated fallback images — design

## Goal

Add a second fallback flavor that produces an image **on demand** from a
hash of the requested key, complementing the existing curated
placeholder pool. The current fallback returns one of a fixed set of
files from `ESSIE3_FALLBACK_DATA_DIR`; for buckets with little overlap
between keys and pool extensions, callers see `NoSuchKey` even though a
synthetic placeholder would be perfectly serviceable for dev/test.

Generated images are **deterministic**: the same key always yields the
same bytes, regardless of process restarts or pool contents. This
preserves the property that callers can rely on the fallback URL as a
stable handle for a missing key.

The generator is pure-stdlib (`image`, `image/color`, `image/draw`,
`image/png`, `image/jpeg`, `crypto/md5`) — no new dependencies, in
line with the project's stdlib-only rule.

## Scope

- New `ESSIE3_FALLBACK_MODE` env var with three values:
  - `pool` — current behavior; only curated placeholders are served.
  - `generate` — only generated images; pool is ignored.
  - `both` — try pool first, generate if no pool match.
  - Default: `pool` (zero behavior change for existing users).
- Generation supports two output formats, driven by the requested
  key's extension: `.png` → PNG, `.jpg` / `.jpeg` → JPEG. Any other
  extension under `generate` mode → `404 NoSuchKey` (do **not** lie
  about the format).
- Generated images are **bubble identicons** in the style of
  [dakridge/identicon](https://github.com/dakridge/identicon): a
  composition of 16 translucent overlapping circles on a white
  background, with positions, sizes, and colors derived from
  `MD5(key)`. Visually distinct per key, no font dependency.
- Generated responses carry a stable `ETag` (MD5 of the bytes, S3
  convention) and a `Last-Modified` header (process start time, shared
  across all generated images of that process).
- `If-Range` / `If-None-Match` work naturally with the generated
  `ETag`, so the existing Range-request and conditional-GET paths
  cover generated images with no new code.

## Architecture

### New file: `generate.go`

```go
package main

import (
    "bytes"
    "crypto/md5"
    "encoding/hex"
    "image"
    "image/color"
    "image/draw"
    "image/jpeg"
    "image/png"
    "path"
    "strings"
)

// generatableExtensions is the set of extensions for which the
// generate mode can produce an image. Keys with any other extension
// fall through to NoSuchKey under mode=generate, and skip the
// generator under mode=both.
var generatableExtensions = map[string]bool{
    ".png": true, ".jpg": true, ".jpeg": true,
}

// generateImage renders a key-seeded bubble identicon for key and
// returns the encoded body plus its Content-Type. Returns (nil, "")
// when the key's extension is not in generatableExtensions.
func generateImage(key string) (body []byte, contentType string)
```

The interesting helpers (all unexported, lowercase, in `generate.go`):

- `bubbleSpec` — a value type for one bubble: `{cx, cy int; radius
  float64; col color.NRGBA}`.
- `bubbleFromDigest(digest [16]byte, i int) bubbleSpec` — derives the
  i-th bubble (0..15) from the MD5 digest using the dakridge layout
  rules (see "Generation algorithm" below).
- `circleMask(r float64) *image.Alpha` — builds an anti-aliased disc
  mask by 4×4 subsampling each pixel of the bounding box. Pixel
  coverage 0..255 fills the `Alpha` image; the result is reused as
  the alpha mask for `draw.DrawMask`.
- `renderIdenticon(digest [16]byte) *image.NRGBA` — fills a 512×512
  NRGBA canvas with white, then iterates 16 bubbles, calling
  `draw.DrawMask(canvas, bounds, &image.Uniform{premulCol},
  image.Point{}, mask, image.Point{}, draw.Over)` for each. Bubble
  alpha is fixed at 0.75 (matching dakridge); the color is
  alpha-premultiplied before compositing.
- `category20` — package-level `[20]color.NRGBA` constant with the
  D3 `category20` palette hardcoded in.

### Modifications to `fallback.go`

`Placeholder` gains two optional fields used only by generated
results:

```go
type Placeholder struct {
    Path        string  // empty for generated
    Body        []byte
    ContentType string
    ETag        string  // empty for pool placeholders, set for generated
    Generated   bool    // true when produced by generateImage
}
```

`Fallback` gains a mode and a Last-Modified timestamp:

```go
type FallbackMode int

const (
    FallbackModePool     FallbackMode = iota // default
    FallbackModeGenerate
    FallbackModeBoth
)

type Fallback struct {
    all          []*Placeholder
    byExt        map[string][]*Placeholder
    inlineExts   map[string]bool
    mode         FallbackMode
    generatedAt  time.Time   // set at NewFallback time, used as Last-Modified
}

// ParseFallbackMode maps "pool"|"generate"|"both" to the constant.
// Empty string returns FallbackModePool. Anything else returns an
// error so main() can fail fast with a clear message.
func ParseFallbackMode(s string) (FallbackMode, error)
```

`NewFallback` signature gains the mode (constructor wiring is the
cheapest way to propagate it without globals):

```go
func NewFallback(dir string, inlineExts []string, mode FallbackMode) (*Fallback, error)
```

`Select` becomes mode-aware:

```go
func (fb *Fallback) Select(key string) *Placeholder {
    switch fb.mode {
    case FallbackModePool:
        return fb.selectFromPool(key)
    case FallbackModeGenerate:
        return fb.generate(key)
    case FallbackModeBoth:
        if p := fb.selectFromPool(key); p != nil {
            return p
        }
        return fb.generate(key)
    }
    return nil
}

// selectFromPool is the body of the current Select implementation.
func (fb *Fallback) selectFromPool(key string) *Placeholder { ... }

// generate returns a freshly-rendered identicon Placeholder or nil
// when the key's extension is not generatable.
func (fb *Fallback) generate(key string) *Placeholder {
    body, ct := generateImage(key)
    if body == nil {
        return nil
    }
    sum := md5.Sum(body)
    return &Placeholder{
        Body:        body,
        ContentType: ct,
        ETag:        `"` + hex.EncodeToString(sum[:]) + `"`,
        Generated:   true,
    }
}

// LastModified returns the Last-Modified time to use on generated
// responses (zero value for pool-only Fallback).
func (fb *Fallback) LastModified() time.Time { return fb.generatedAt }
```

`Disposition` is unchanged — it already keys off the request's
extension, which is the same whether we're serving pool or generated
bytes.

### Modifications to `main.go`

Read the new env var, parse it, pass it to `NewFallback`, and print
the resolved mode in startup output. Failing to parse the mode is
fatal — matches the existing pattern for fallback-data load failures.

```go
modeStr := os.Getenv("ESSIE3_FALLBACK_MODE")
mode, err := ParseFallbackMode(modeStr)
if err != nil {
    log.Fatalf("invalid ESSIE3_FALLBACK_MODE %q: %v", modeStr, err)
}
fallback, err := NewFallback(fallbackDataDir, inlineExts, mode)
```

Startup banner adds one line:

```
  fallback mode: pool | generate | both
```

### Modifications to `handler.go`

Three small additions inside the **fallback branches** of
`handleGetObject` and `handleHeadObject` (the existing
`if p := h.fallback.Select(key); p != nil { ... }` blocks):

1. If `p.ETag != ""`, set `ETag: <p.ETag>` on the response.
2. If `p.Generated`, set `Last-Modified: <h.fallback.LastModified()>`.
3. Pass `p.ETag` (instead of an empty string) into the existing
   `evaluateRange(r, totalLen, p.ETag)` call so `If-Range` honors the
   generated ETag.

The 200 / 206 / 416 / auth-error response shape is unchanged — only
the per-response header set widens.

`handleGetObject` and `handleHeadObject` already share the same
fallback-branch shape; the three additions go to both sites.

## Configuration

### `ESSIE3_FALLBACK_MODE`

| Value      | Pool consulted? | Generate consulted? | Default |
|------------|-----------------|---------------------|---------|
| `pool`     | yes             | no                  | ✓       |
| `generate` | no              | yes                 |         |
| `both`     | yes (first)     | yes (fallback)      |         |

Unset / empty → `pool` (existing behavior preserved).
Invalid value → fatal startup error.

### Existing env vars

- `ESSIE3_FALLBACK_DATA_DIR` — still consulted in `pool` and `both`
  modes. In `generate` mode the directory may be empty or missing
  without warning.
- `ESSIE3_FALLBACK_INLINE_EXTENSIONS` — applies uniformly to pool and
  generated responses (both go through `Disposition`).

## Generation algorithm

The bubble identicon mirrors dakridge/identicon's algorithm: 16
translucent overlapping circles whose positions, sizes, and colors
are derived from `MD5(key)`. We render it server-side in Go with
hand-rolled anti-aliasing on top of `image/draw`.

Constants (all in `generate.go`):

- `canvasSize = 512` — output is 512×512.
- `gridDivisions = 16` — bubble centers sit on a 16×16 grid (cell
  size = 32 px).
- `minRadius = 6.0`, `maxRadius = float64(canvasSize) / 2` (= 256) —
  radii sweep linearly between these.
- `bubbleAlpha = 0.75` — per-bubble opacity, applied via
  alpha-premultiplied color in `draw.DrawMask` with `draw.Over`.
- `bubbleCount = 16` — number of bubbles drawn per image.
- `category20 [20]color.NRGBA` — D3 categorical 20-color palette,
  reproduced byte-for-byte.

Per-image steps:

1. **Digest.** `digest := md5.Sum([]byte(key))` — 16 bytes.
2. **Canvas.** 512×512 NRGBA, filled white.
3. **Per-bubble derivation.** For `i = 0..15`, using `b = digest[i]`:
   - `x := int(b >> 4)`           — column 0..15
   - `y := int(b & 0x0F)`         — row 0..15
   - Center: `cx = x * 32`, `cy = y * 32` (32 = canvasSize / 16).
   - Radius nibble index: `idx := (x * y) % 32`. Read the nibble at
     hex position `idx` of the digest — high nibble of
     `digest[idx/2]` if `idx%2 == 0`, low nibble otherwise. Let
     `n = nibble` (0..15).
   - Radius: `r = minRadius + (float64(n) / 15.0) * (maxRadius -
     minRadius)`.
   - Color: `col = category20[(x + y) % 20]`.
4. **Render bubble.** Build an `*image.Alpha` mask of size
   `2*ceil(r) × 2*ceil(r)` by 4×4 subsampling: for each pixel `(px,
   py)` in the mask, take 16 sub-samples uniformly across the pixel,
   count how many satisfy `dx² + dy² ≤ r²` (relative to the mask
   center), and set the pixel's alpha to `count * 255 / 16`.
   Compose:

   ```go
   premul := color.NRGBA{col.R, col.G, col.B, uint8(bubbleAlpha*255)}
   draw.DrawMask(canvas,
       image.Rect(cx-int(r), cy-int(r), cx+int(r), cy+int(r)),
       &image.Uniform{premul}, image.Point{},
       mask, image.Point{}, draw.Over)
   ```

   `draw.DrawMask` automatically clips to the canvas, so bubbles
   centered near edges (e.g., x or y = 0) render only their visible
   portion.
5. **Encode.** `.png` → `png.Encode(buf, canvas)`. `.jpg`/`.jpeg` →
   `jpeg.Encode(buf, canvas, &jpeg.Options{Quality: 85})`.
6. **ETag.** `etag := MD5(buf.Bytes())`, wrapped in double quotes
   — 32-hex-char S3 single-PUT shape. This is a *second* MD5 pass,
   over the encoded body; the first MD5 over the key drives the
   layout. Both are cheap.

### Faithful-to-dakridge quirks worth flagging

- **Zero-product radius.** Because the radius nibble index is `(x *
  y) % 32`, every bubble in iteration `i` where `digest[i]`'s high
  *or* low nibble is zero reads the same nibble (position 0) for
  its radius — so those bubbles all share one size per image. This
  is a property of the source algorithm; we preserve it.
- **No per-bubble jitter beyond grid snap.** Centers are always
  exact multiples of 32 px. Visual variety comes from radius,
  color, and overlap — not from sub-cell positioning.
- **Palette is fixed.** The D3 category20 palette has a few
  low-saturation pastel entries (light blue, peach, light green,
  …) that can wash out against the white background when their
  bubble is small. We accept this — it's part of the look.

Dimensions, palette, bubble count, alpha, and JPEG quality are
constants for v1. Promoting any of them to env vars is in **Out of
scope**.

## Response shape

For a generated response on GET (the HEAD shape drops the body):

```
HTTP/1.1 200 OK
Accept-Ranges: bytes
Content-Type: image/png
Content-Length: <n>
Content-Disposition: inline; filename="<basename>"
ETag: "<md5 hex>"
Last-Modified: <process start, RFC 1123>
<body>
```

Range responses use the same shape with `206`, `Content-Range`, and a
sliced body — handled by the existing `evaluateRange` plumbing once
the generated `ETag` flows through.

The auth path is unchanged: a generated response is a fallback
response, so `FallbackPublic` and the existing `objectACL=""`
treatment apply exactly as today.

## Mode interaction matrix

For a GET on a missing key, given mode and key extension:

| Mode       | Pool has match | Generatable ext | Result                |
|------------|----------------|-----------------|-----------------------|
| `pool`     | yes            | —               | pool placeholder      |
| `pool`     | no             | —               | `404 NoSuchKey`       |
| `generate` | —              | yes             | generated image       |
| `generate` | —              | no              | `404 NoSuchKey`       |
| `both`     | yes            | —               | pool placeholder      |
| `both`     | no             | yes             | generated image       |
| `both`     | no             | no              | `404 NoSuchKey`       |

(The "Pool has match" column is only meaningful for modes that
consult the pool.)

## File changes summary

- **Create** `generate.go` — bubble layout derivation, anti-aliased
  circle mask, identicon rendering, PNG/JPEG encoding, hardcoded
  `category20` palette.
- **Create** `generate_test.go` — pure-function tests against
  `generateImage`, `bubbleFromDigest`, `circleMask`.
- **Modify** `fallback.go` — `FallbackMode` type and parser, mode
  field on `Fallback`, `LastModified` accessor, `Generated`/`ETag`
  fields on `Placeholder`, mode-aware `Select`, `selectFromPool` (the
  extracted current body), `generate` method. Imports gain `time`,
  `crypto/md5`, `encoding/hex`.
- **Modify** `main.go` — read `ESSIE3_FALLBACK_MODE`, pass mode to
  `NewFallback`, print mode in startup banner.
- **Modify** `handler.go` — set `ETag` and `Last-Modified` on
  generated fallback responses; thread `p.ETag` into `evaluateRange`
  on both fallback branches (`handleGetObject` and
  `handleHeadObject`).
- **Modify** `handler_test.go` — integration tests for each mode.
- **Modify** `fallback_test.go` — unit tests for mode parsing,
  mode-aware `Select`, extension filtering.
- **Modify** `README.md` — short subsection under Fallback documenting
  the new env var and the three modes.

## Testing

### Unit tests in `generate_test.go`

- `generateImage` returns non-nil body and the right `Content-Type`
  for `.png`, `.jpg`, `.jpeg`. Nil for `.gif`, `.pdf`, `.txt`, no
  extension.
- Determinism: two calls for the same key produce byte-identical
  output for both PNG and JPEG.
- Distinctness: two different keys produce different bytes.
- Decoded PNG/JPEG output has the expected 512×512 dimensions.
- `bubbleFromDigest` for known digests returns the expected centers,
  radii, and palette colors — table-driven, hand-computed on a few
  fixed digests so the algorithm doesn't silently drift.
- `circleMask(r)`:
  - Mask dimensions are `2*ceil(r) × 2*ceil(r)`.
  - Center pixel alpha == 255 (fully inside).
  - Far-corner pixels (well outside the disc) alpha == 0.
  - Boundary pixels (on the radius) have intermediate alpha
    (0 < a < 255), confirming the 4×4 subsampling is wired up.
- Rendered canvas is not uniformly white — at least one pixel in the
  interior differs from the white background (smoke test that
  bubbles were actually composited).

### Unit tests in `fallback_test.go`

- `ParseFallbackMode`: each valid string maps to the expected
  constant; empty string returns `FallbackModePool`; arbitrary
  garbage returns an error.
- `Select` under `FallbackModePool` matches today's behavior — pool
  hit returns pool, no pool match returns nil regardless of
  extension.
- `Select` under `FallbackModeGenerate` returns a generated
  Placeholder for `.png`/`.jpg`/`.jpeg`, nil for `.pdf`/`.txt`/no
  extension, even when the pool would have matched.
- `Select` under `FallbackModeBoth`: pool hit returns pool, pool miss
  + generatable ext returns generated, pool miss + non-generatable
  ext returns nil.
- A generated `Placeholder`'s `ETag` matches `"" + hex(md5(Body)) +
  ""` — confirms the ETag wiring matches S3 single-PUT convention.

### Integration tests in `handler_test.go`

Through `httptest.NewServer(NewHandler(...))` with a `Fallback` built
in each mode:

- `mode=generate`, GET a missing `.png` key → 200, `Content-Type:
  image/png`, body decodes as a 512×512 PNG, `ETag` is present and
  quoted-hex, `Last-Modified` is set.
- `mode=generate`, GET a missing `.jpg` key → 200, decodes as JPEG.
- `mode=generate`, GET a missing `.pdf` key (no pool either) → 404
  `NoSuchKey`.
- `mode=generate`, HEAD a missing `.png` key → 200, headers identical
  to GET, empty body.
- `mode=generate`, GET with `Range: bytes=0-99` on a missing `.png`
  → 206, `Content-Range: bytes 0-99/<total>`.
- `mode=generate`, GET with `If-None-Match: <correct ETag>` → 304.
- `mode=generate`, two GETs on the same key → byte-identical
  responses.
- `mode=both`, GET a missing key whose extension is in the pool →
  pool placeholder (existing fallback test shape, asserted by body
  content matching one of the pool entries).
- `mode=both`, GET a missing `.png` key with **no** pool entry for
  `.png` → generated image.
- `mode=pool` (default), no behavior change — existing fallback
  tests pass unchanged.

The existing `TestHandler_GetObject_Fallback*` tests run in default
`mode=pool` and should remain green untouched.

## Out of scope

- Caching generated bytes across requests (would help HEAD-then-GET
  patterns, but each generation is cheap and this is a dev tool).
- Env-configurable image dimensions / bubble count / alpha / JPEG
  quality (the constants in `generate.go` cover the dev-server use
  case; promote to env vars if a need surfaces).
- Generating formats beyond PNG/JPEG (e.g. WebP, GIF). WebP isn't
  available in stdlib; GIF would work but adds palette quantization
  complexity for marginal value.
- Text overlay (the key, dimensions, "404") — requires a font, which
  pulls in `golang.org/x/image/font` and breaks the stdlib-only rule.
- Backfilling ETag/`Last-Modified` for pool placeholders. Could be a
  separate cleanup; not coupled to this feature.
- Per-bucket mode overrides. Single global `ESSIE3_FALLBACK_MODE` for
  now.
