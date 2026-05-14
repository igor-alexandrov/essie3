# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
go run .                       # run the server (reads env vars; see README "Configuration")
go test ./...                  # full test suite
go test -race -count=1 ./...   # what CI runs; use this before pushing
go test -run TestName -v       # run a single test (regex)
go vet ./...
gofmt -l .                     # CI fails if this lists any files; fix with `gofmt -w .`
go mod tidy                    # CI fails if this would change go.mod/go.sum
```

Standard library only — `go.mod` declares no third-party deps and CI enforces `go mod tidy`.

## Architecture

Single `main` package; every `.go` file lives in the repo root (no subdirectories). The server is composed in `main.go` by wiring four pieces into an `http.Handler`:

- **`storage.go`** — filesystem layer. Objects live at `data/<bucket>/<key>` with a `<key>.meta.json` sidecar. Writes go through `writeFileAtomic` (temp file + rename) so a crash or concurrent writer can't leave a torn file. `validateName` rejects empty, absolute, or `..`-bearing names and **must run before any filesystem access** — it is the path-traversal defense.
- **`fallback.go`** — when a GET/HEAD misses, `Fallback.Select(key)` picks a placeholder deterministically via an FNV-32a hash of the key. Same key → same placeholder. Pools are indexed by extension with `extAliases` collapsing variants (e.g. `.jpeg` → `.jpg`). `Disposition(key)` returns `inline; filename=...` or `attachment; filename=...` based on `FALLBACK_INLINE_EXTENSIONS`.
- **`auth.go`** — opt-in via `ESSIE3_ACCESS_KEY`. Parses only the `Credential=` portion of the SigV4 `Authorization` header; **signatures are not verified**. `authorize(r, op, objectACL)` returns `nil` on allow or `*authError` (HTTP status + S3 error code) on deny. The `public-read` ACL is the read escape hatch; reads pre-load `ObjectMeta` so `handleGetObject` / `handleHeadObject` can pass the stored ACL into `authorize`.
- **`handler.go`** — `ServeHTTP` splits `/bucket/key`, dispatches by HTTP method, and calls `auth.authorize` at the top of each branch. CORS headers are set on every response.

**S3-shaped XML errors:** Never return plain HTTP errors from handlers. Use `writeXMLError` / `writeNoSuchKey` / `writeNoSuchBucket` from `xml.go`, or `writeAuthError` from `auth.go` — clients expect S3's `<Error><Code>...` shape.

**This is not a production S3 replacement:** no SigV4 signature verification, no object versioning, no real `ListObjects`. It exists to stand in for AWS S3 in local dev and integration tests.

## Testing patterns

- Tests live next to source as `<name>_test.go`. Standard `testing` package only — no test framework.
- Handler/auth tests spin up a real server via `httptest.NewServer(NewHandler(...))`. Use the helpers in `handler_test.go`: `testServer(t)` for auth-off, `testServerWithAuth(t, AuthConfig{...})` for auth-on.
- Fallback fixtures live in `testdata/fallback/`.

## Go imports are per-file

When deleting the last use of an imported package from a `.go` file, also remove that import from *that file's* import block — Go imports are per-file, not per-package, so the build will fail with `"<pkg>" imported and not used`. Other files in the package may legitimately still import the same package.

## Workflow artifacts

Design specs and implementation plans for feature work are committed under `docs/superpowers/specs/` and `docs/superpowers/plans/` respectively. Existing examples there are good shape references for new specs/plans.

## CI

`.github/workflows/ci.yml` runs `go build`, `go vet`, `go test -race -count=1 ./...`, `gofmt -l .`, and a `go mod tidy` diff check on every PR. Run the equivalent locally before pushing.
