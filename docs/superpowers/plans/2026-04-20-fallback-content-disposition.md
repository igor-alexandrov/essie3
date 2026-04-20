# Fallback Content-Disposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When essie3 serves a fallback placeholder, it should return `Content-Disposition` with the requested key's basename as filename, and use `inline` for a configurable list of extensions (default: current image/video/PDF set) and `attachment` for everything else.

**Architecture:** A new exported `DefaultInlineExtensions` slice holds the current media defaults. `NewFallback` takes a `[]string` of inline extensions. A new `(*Fallback).Disposition(key)` method computes the header value. Handler fallback branches for GET and HEAD set it. Main reads `FALLBACK_INLINE_EXTENSIONS` env var (unset → defaults, empty string → no inlines).

**Tech Stack:** Go 1.22, standard library only (`path`, `path/filepath`, `strings`, `fmt`).

---

## File Structure

- **Modify** `fallback.go` — add `DefaultInlineExtensions`, `ParseExtList`, `normalizeExtInput`, `inlineExts` field, change `NewFallback` signature, add `Disposition` method.
- **Modify** `handler.go` — set `Content-Disposition` header in GET and HEAD fallback branches.
- **Modify** `main.go` — read env var and pass to `NewFallback`.
- **Modify** `fallback_test.go` — update existing `NewFallback(dir)` calls → `NewFallback(dir, DefaultInlineExtensions)`; add `TestParseExtList`, `TestFallbackDisposition_Inline`, `TestFallbackDisposition_Attachment`, `TestFallbackDisposition_CustomList`.
- **Modify** `handler_test.go` — update `testServer` helper; extend GET/HEAD fallback assertions with `Content-Disposition`.
- **Modify** `README.md` — add `FALLBACK_INLINE_EXTENSIONS` to env var table.

---

### Task 1: Add `ParseExtList` and `DefaultInlineExtensions`

Pure functions with no struct dependencies — easiest first step.

**Files:**
- Modify: `fallback.go`
- Test: `fallback_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `fallback_test.go`:

```go
func TestParseExtList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"jpg", []string{".jpg"}},
		{".jpg", []string{".jpg"}},
		{"JPG", []string{".jpg"}},
		{" .jpg , png ,  WEBP ", []string{".jpg", ".png", ".webp"}},
		{",,", []string{}},
	}
	for _, c := range cases {
		got := ParseExtList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("ParseExtList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseExtList(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestDefaultInlineExtensions_CoversCurrentFallbackSet(t *testing.T) {
	want := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf", ".mp4", ".mov", ".webm", ".avi"}
	set := map[string]bool{}
	for _, e := range DefaultInlineExtensions {
		set[e] = true
	}
	for _, e := range want {
		if !set[e] {
			t.Errorf("DefaultInlineExtensions missing %q", e)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestParseExtList|TestDefaultInlineExtensions' -v`
Expected: FAIL — `undefined: ParseExtList`, `undefined: DefaultInlineExtensions`.

- [ ] **Step 3: Implement in `fallback.go`**

Add these declarations (place the var near the top with `placeholderExtensions`, functions near the bottom with other helpers):

```go
// DefaultInlineExtensions is the set of extensions served with
// Content-Disposition: inline when a fallback placeholder is returned.
// Extensions outside this set (e.g. future docx/xlsx placeholders) are
// served as attachments so browsers prompt a download.
var DefaultInlineExtensions = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp",
	".pdf",
	".mp4", ".mov", ".webm", ".avi",
}

// normalizeExtInput accepts ".jpg", "jpg", " JPG " and returns ".jpg".
// Empty/whitespace input returns "".
func normalizeExtInput(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// ParseExtList splits a comma-separated list of extensions and normalizes
// each one. Returns a non-nil slice so callers can distinguish "unset"
// (nil) from "explicitly empty".
func ParseExtList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if e := normalizeExtInput(part); e != "" {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestParseExtList|TestDefaultInlineExtensions' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add fallback.go fallback_test.go
git commit -m "Add ParseExtList and DefaultInlineExtensions"
```

---

### Task 2: Change `NewFallback` signature and add `Disposition` method

Introduces the struct field and signature change. All call sites must be updated in this task so tests still compile.

**Files:**
- Modify: `fallback.go`
- Modify: `fallback_test.go`
- Modify: `handler_test.go`
- Modify: `main.go` (temporary — revisited in Task 5)

- [ ] **Step 1: Write the failing tests**

Append to `fallback_test.go`:

```go
func TestFallbackDisposition_InlineForDefaults(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions)
	got := fb.Disposition("photos/sunset.jpg")
	want := `inline; filename="sunset.jpg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_AttachmentForUnlisted(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions)
	got := fb.Disposition("docs/report.docx")
	want := `attachment; filename="report.docx"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_CustomList(t *testing.T) {
	// Explicit empty list → everything is attachment.
	fb, _ := NewFallback("testdata/fallback", []string{})
	got := fb.Disposition("images/a.jpg")
	want := `attachment; filename="a.jpg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}

	// Custom list adds docx as inline.
	fb2, _ := NewFallback("testdata/fallback", []string{".docx"})
	got = fb2.Disposition("reports/q1.docx")
	want = `inline; filename="q1.docx"`
	if got != want {
		t.Fatalf("Disposition custom = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_JpegAliasedToJpg(t *testing.T) {
	// Inline list contains .jpg; requesting .jpeg should still be inline.
	fb, _ := NewFallback("testdata/fallback", []string{".jpg"})
	got := fb.Disposition("pic.jpeg")
	want := `inline; filename="pic.jpeg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Update existing callers so the file compiles**

In `fallback_test.go`, replace every `NewFallback("testdata/fallback")` with `NewFallback("testdata/fallback", DefaultInlineExtensions)`. There are calls in:
- `TestFallbackLoad`
- `TestFallbackSelect_Deterministic`
- `TestFallbackSelect_DifferentKeys`
- `TestFallbackSelect_MatchesExtension`
- `TestFallbackSelect_NilForUnmatchedExtension`

Also update the `NewFallback(dir)` call in `TestFallbackLoad_EmptyDir` and `TestFallbackSelect_NoImages`:

```go
fb, err := NewFallback(dir, DefaultInlineExtensions)
```

In `handler_test.go:17`, update `testServer`:

```go
fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions)
```

In `main.go`, update the `NewFallback` call (will be revisited in Task 5):

```go
fallback, err := NewFallback(fallbackDataDir, DefaultInlineExtensions)
```

- [ ] **Step 3: Run tests to verify the new ones fail**

Run: `go test ./... -run 'TestFallbackDisposition' -v`
Expected: FAIL — `fb.Disposition undefined`.

- [ ] **Step 4: Implement `inlineExts` field, updated `NewFallback`, and `Disposition`**

Replace the `Fallback` struct, `NewFallback`, and add `Disposition` in `fallback.go`:

```go
type Fallback struct {
	all        []*Placeholder
	byExt      map[string][]*Placeholder
	inlineExts map[string]bool
}

func NewFallback(dir string, inlineExts []string) (*Fallback, error) {
	fb := &Fallback{
		byExt:      make(map[string][]*Placeholder),
		inlineExts: make(map[string]bool, len(inlineExts)),
	}
	for _, e := range inlineExts {
		fb.inlineExts[normalizeExt(normalizeExtInput(e))] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fb, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !placeholderExtensions[ext] {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(fullPath)
		if err != nil {
			log.Printf("fallback: skip %s: %v", fullPath, err)
			continue
		}
		p := &Placeholder{
			Path:        fullPath,
			Body:        body,
			ContentType: http.DetectContentType(body),
		}
		canonical := normalizeExt(ext)
		fb.all = append(fb.all, p)
		fb.byExt[canonical] = append(fb.byExt[canonical], p)
	}

	return fb, nil
}

// Disposition returns the Content-Disposition header value for a fallback
// response to the given key. Extensions in the inline set are served
// inline; everything else is an attachment. The filename is the basename
// of the requested key so downloads preserve the caller's intent.
func (fb *Fallback) Disposition(key string) string {
	name := path.Base(key)
	ext := normalizeExt(strings.ToLower(filepath.Ext(key)))
	kind := "attachment"
	if fb.inlineExts[ext] {
		kind = "inline"
	}
	return fmt.Sprintf("%s; filename=%q", kind, name)
}
```

Add these imports to `fallback.go`:

```go
import (
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)
```

- [ ] **Step 5: Run all tests**

Run: `go test ./... -v`
Expected: PASS — all existing tests still pass and new `TestFallbackDisposition_*` tests pass.

- [ ] **Step 6: Commit**

```bash
git add fallback.go fallback_test.go handler_test.go main.go
git commit -m "Add Fallback.Disposition and inlineExts configuration"
```

---

### Task 3: Wire `Disposition` into `handleGetObject` fallback branch

**Files:**
- Modify: `handler.go:139-151` (the `handleGetObject` fallback-branch block)
- Modify: `handler_test.go` (extend `TestHandler_GetObject_FallbackImage`)

- [ ] **Step 1: Update the existing test to assert the new header**

Edit `TestHandler_GetObject_FallbackImage` in `handler_test.go`. After the existing `Content-Type` check, add:

```go
	cd := resp.Header.Get("Content-Disposition")
	want := `inline; filename="photo.jpg"`
	if cd != want {
		t.Fatalf("Content-Disposition = %q, want %q", cd, want)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestHandler_GetObject_FallbackImage -v`
Expected: FAIL — empty `Content-Disposition`.

- [ ] **Step 3: Set the header in `handler.go`**

In `handleGetObject`, inside the fallback branch, add the `Content-Disposition` line:

```go
	if p := h.fallback.Select(key); p != nil {
		w.Header().Set("Content-Type", p.ContentType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(p.Body)))
		w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
		w.WriteHeader(http.StatusOK)
		w.Write(p.Body)
		return
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestHandler_GetObject_FallbackImage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add handler.go handler_test.go
git commit -m "Set Content-Disposition on fallback GET responses"
```

---

### Task 4: Wire `Disposition` into `handleHeadObject` fallback branch

**Files:**
- Modify: `handler.go:164-175` (the `handleHeadObject` fallback-branch block)
- Modify: `handler_test.go` (extend `TestHandler_HeadObject_FallbackImage`)

- [ ] **Step 1: Update the existing test**

Edit `TestHandler_HeadObject_FallbackImage` in `handler_test.go`. After the status-code check, add:

```go
	cd := resp.Header.Get("Content-Disposition")
	want := `inline; filename="photo.jpg"`
	if cd != want {
		t.Fatalf("Content-Disposition = %q, want %q", cd, want)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestHandler_HeadObject_FallbackImage -v`
Expected: FAIL — empty `Content-Disposition`.

- [ ] **Step 3: Set the header in `handler.go`**

In `handleHeadObject`, inside the fallback branch, add:

```go
	if p := h.fallback.Select(key); p != nil {
		w.Header().Set("Content-Type", p.ContentType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(p.Body)))
		w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
		w.WriteHeader(http.StatusOK)
		return
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestHandler_HeadObject_FallbackImage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add handler.go handler_test.go
git commit -m "Set Content-Disposition on fallback HEAD responses"
```

---

### Task 5: Wire `FALLBACK_INLINE_EXTENSIONS` env var in `main.go`

No test — `main.go` has no test coverage in this repo. Verify by build + manual check of the log line.

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Read the env var with distinct unset-vs-empty semantics**

Replace the fallback-construction block in `main.go`. Locate:

```go
	fallback, err := NewFallback(fallbackDataDir, DefaultInlineExtensions)
```

Replace with:

```go
	inlineExts := DefaultInlineExtensions
	if v, ok := os.LookupEnv("FALLBACK_INLINE_EXTENSIONS"); ok {
		inlineExts = ParseExtList(v)
	}
	fallback, err := NewFallback(fallbackDataDir, inlineExts)
```

- [ ] **Step 2: Build and run a quick sanity check**

Run: `go build ./... && go vet ./...`
Expected: no output.

Run: `go test ./... -count=1`
Expected: PASS (all).

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "Read FALLBACK_INLINE_EXTENSIONS from environment"
```

---

### Task 6: Document the new env var in `README.md`

**Files:**
- Modify: `README.md` (env var table in the Configuration section)

- [ ] **Step 1: Add the new row**

In `README.md`, locate the configuration table:

```
| Variable            | Default            | Description                       |
| ------------------- | ------------------ | --------------------------------- |
| `PORT`              | `9000`             | HTTP port to listen on            |
| `DATA_DIR`          | `./data`           | Where uploaded objects are stored |
| `FALLBACK_DATA_DIR` | `./fallback-data`  | Directory of fallback placeholders|
```

Append a row:

```
| `FALLBACK_INLINE_EXTENSIONS` | media set (`.jpg,.jpeg,.png,.gif,.webp,.pdf,.mp4,.mov,.webm,.avi`) | Comma-separated extensions served inline on fallback responses. Everything else is served as `attachment`. Set to empty string to serve all fallbacks as attachments. |
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "Document FALLBACK_INLINE_EXTENSIONS in README"
```

---

## Self-Review

- **Spec coverage:** every user requirement maps to a task —
  - "All current extensions served inline" → Task 1 (`DefaultInlineExtensions`) + Task 3/4 (wiring).
  - "Future extensions served as attachments" → Task 2 (`Disposition` default branch) + Task 3/4 (wiring) + Task 6 (docs).
  - "Configurable via variable" → Task 1 (`ParseExtList`) + Task 5 (env var).
- **Placeholders:** no TBDs, every code step has complete code.
- **Type consistency:** `ParseExtList`, `normalizeExtInput`, `DefaultInlineExtensions`, `Disposition`, `inlineExts` names match across tasks.

Known follow-ups outside scope: RFC 5987 `filename*=UTF-8''…` for non-ASCII filenames, POST multipart `Content-Disposition` capture (separate from fallback behavior), ETag/Last-Modified on fallback responses.
