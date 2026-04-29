# Auth and ACL Design

**Status:** Draft — design only, no implementation plan yet.

**Goal:** Let essie3 optionally enforce a single configured access key on incoming requests, so integration test suites can assert "unauthenticated requests get 403" against essie3 the same way they would against real S3. When the feature is off (current default), behavior is unchanged.

## Background

essie3 is a filesystem-backed S3 stand-in for local development. Today it captures `x-amz-acl` into object metadata (`handler.go:121`, `storage.go:19`) but never enforces it, and performs no request authentication — AWS SDKs work because their signatures are ignored, not validated.

The target use case is **integration tests that exercise the auth-failure path**. That is narrower than "make essie3 production-safe" and drives every decision below.

## Non-Goals

- **Full SigV4 signature verification.** We parse the access key out of the `Authorization` header but do not recompute or compare signatures. Validating SigV4 is not a useful signal for the stated use case and is a maintenance burden.
- **Multiple identities.** A single configured key. Multi-tenant scenarios are not in scope.
- **A full ACL model.** Only `public-read` is honored as a distinct ACL value. `private`, `authenticated-read`, `bucket-owner-*`, owner/grantee structures, etc. are out.
- **Bucket-level ACLs.** Bucket ops always require auth when auth is enabled.
- **Presigned URLs.** Only the `Authorization` header form is inspected. Query-string credentials (`?X-Amz-Credential=…&X-Amz-Signature=…`) are not recognized; a test needing them can be added later.

## Configuration

Two new environment variables, consumed once at startup in `main.go`, passed into an `AuthConfig` that flows into the handler.

| Variable | Default | Effect |
| --- | --- | --- |
| `ESSIE3_ACCESS_KEY` | *(unset)* | Unset → auth disabled (current behavior, backwards compatible). Set → auth enabled; requests must present this key in their `Authorization` header. |
| `ESSIE3_FALLBACK_PUBLIC` | `false` | Only consulted when auth is enabled. `true` → fallback placeholders are served anonymously even without credentials. `false` → fallbacks follow the same auth check as real objects. |

```go
type AuthConfig struct {
    AccessKey      string // empty = auth disabled
    FallbackPublic bool
}

func (c AuthConfig) Enabled() bool { return c.AccessKey != "" }
```

Startup log line includes whether auth is on so it's obvious at boot.

## Identity Check

Pure function living in a new `auth.go`:

```go
func (c AuthConfig) checkIdentity(r *http.Request) authResult
```

Returns one of five `authResult` values — the extra granularity exists so the error translator can emit the right S3 error code for each failure mode:

- `authNotRequired` — auth is disabled.
- `authOK` — key matched.
- `authMissing` — no `Authorization` header.
- `authMalformed` — header present but not a parseable SigV4 string.
- `authWrongKey` — parseable SigV4 but the access key doesn't match the configured value.

**Parsing:**

1. If `!c.Enabled()` → `authNotRequired`.
2. Read `Authorization` header. Empty → `authMissing`.
3. Must start with `AWS4-HMAC-SHA256 `. Otherwise → `authMalformed`. (Refuse non-SigV4 schemes rather than silently allowing Basic/Bearer/etc.)
4. Find `Credential=` in the comma-separated parameter list; take the substring before the first `/`. Missing → `authMalformed`.
5. Compare the parsed key to `c.AccessKey` using `subtle.ConstantTimeCompare`. Match → `authOK`; otherwise → `authWrongKey`.

What this check deliberately does **not** do: recompute signatures, validate the date/region scope, enforce clock skew, cross-check `SignedHeaders`.

## Authorization

Every handler operation maps to one of two ops:

| Op | Handlers | Rule |
| --- | --- | --- |
| `opRead`  | `handleGetObject`, `handleHeadObject` | Allowed if auth succeeded OR the object's stored ACL is `public-read`. |
| `opWrite` | `handlePutObject`, `handleCopyObject`, `handlePostObject`, object `DELETE`, `CreateBucket`, bucket `HEAD`, bucket `GET` (list stub) | Allowed only if auth succeeded. |

Bucket ops go into `opWrite` — the strict category — because bucket-level ACLs are out of scope and HEAD/GET bucket are uncommon enough anonymously that requiring auth is fine.

**The authorize function:**

```go
func (c AuthConfig) authorize(r *http.Request, op op, objectACL string) *authError
```

Returns `nil` on allow. On deny, returns `*authError` carrying the HTTP status and S3 error code for the caller to translate via `writeXMLError`. (Using a typed error rather than `error` so handler call sites can pattern-match without string-sniffing.)

Logic:

1. Call `checkIdentity(r)`.
2. `authOK` or `authNotRequired` → return nil.
3. `authMalformed` → deny with 400 `InvalidArgument`.
4. `authWrongKey` → deny with 403 `InvalidAccessKeyId`.
5. `authMissing`:
   - `op == opRead` and `objectACL == "public-read"` → return nil (public-read escape hatch).
   - Otherwise → deny with 403 `AccessDenied`.

Note that the public-read escape hatch only applies when the header is entirely missing — a request that *attempts* auth with a wrong or malformed key gets the corresponding specific error, not silently demoted to anonymous. This matches the likely test intent: "a broken client shouldn't look healthy."

**Call sites.** Inline at the top of each handler method, before doing work.

**Chicken-and-egg for reads.** The ACL lives in the object's metadata, behind the auth check. Resolution: read metadata first (cheap filesystem stat + small JSON read), then call `authorize` with the ACL string. If the object doesn't exist, pass `""` for ACL and let the normal not-found path run *after* auth passes. An unauthenticated reader therefore can't distinguish "key exists but is private" from "key doesn't exist" — same 403 either way. Matches S3's behavior.

**Fallback interaction.** When a GET misses and would fall through to the fallback, check `ESSIE3_FALLBACK_PUBLIC`:

- `true` → serve the fallback without auth regardless of the auth check.
- `false` → the normal auth check already ran with `objectACL = ""`; if it failed, we never got here, if it passed, serve the fallback normally.

## Error Semantics

Uses the existing `writeXMLError(w, status, code, msg, bucket, key)` helper — no new infrastructure.

| Condition | Status | S3 error code |
| --- | --- | --- |
| `Authorization` header missing | 403 | `AccessDenied` |
| Header present but malformed or non-SigV4 scheme | 400 | `InvalidArgument` |
| Access key doesn't match configured value | 403 | `InvalidAccessKeyId` |
| Authenticated but op denied by ACL rules | 403 | `AccessDenied` |

Distinct `AccessDenied` vs `InvalidAccessKeyId` lets tests assert exactly which failure the SDK saw — "creds wrong" versus "no permission." Both match S3's documented codes.

**Logging.** On denial, add a line alongside the existing method/path log: `auth denied: <reason>` where reason is one of the four rows above. Do not log the presented access key — could be real creds mistakenly pointed at this server.

## Architecture

Single new file `auth.go` holding `AuthConfig`, `authResult`, `op`, `checkIdentity`, and `authorize`. The handler gains an `auth AuthConfig` field; each method calls `h.auth.authorize(...)` at its top and returns early on denial.

```
main.go
  └─ reads ESSIE3_ACCESS_KEY, ESSIE3_FALLBACK_PUBLIC
  └─ constructs AuthConfig
  └─ passes into NewHandler

handler.go
  └─ Handler gains `auth AuthConfig`
  └─ each handleXxx method: authorize() at top

auth.go          ← new
  ├─ AuthConfig, authResult, op
  ├─ checkIdentity(r) → authResult
  └─ authorize(r, op, objectACL) → error
```

Files touched:

- **New:** `auth.go`, `auth_test.go`.
- **Modify:** `main.go` (env reads, pass `AuthConfig`), `handler.go` (add field, insert authorize calls, wire ACL reads), `README.md` (document env vars, adjust the "no auth" disclaimer).
- **Unchanged:** `storage.go`, `fallback.go`, `xml.go`, existing tests.

## Testing

Table-driven tests in a new `auth_test.go`. Five groupings:

1. **`checkIdentity` unit tests.** Pure function over `AuthConfig` + synthesized `http.Request`. Cases:
   - Auth disabled, no header → `authNotRequired`.
   - Auth disabled, bogus header → `authNotRequired` (disabled means don't look).
   - Enabled, no header → `authMissing`.
   - Enabled, non-SigV4 scheme (`Basic foo`) → `authMalformed`.
   - Enabled, SigV4 prefix but no `Credential=` → `authMalformed`.
   - Enabled, correct key → `authOK`.
   - Enabled, wrong key → `authWrongKey`.

2. **`authorize` unit tests.** Matrix over `{op, identity result, objectACL}`:
   - `(*, authOK, *)` → nil.
   - `(*, authNotRequired, *)` → nil.
   - `(*, authMalformed, *)` → 400 `InvalidArgument`.
   - `(*, authWrongKey, *)` → 403 `InvalidAccessKeyId`.
   - `(opWrite, authMissing, *)` → 403 `AccessDenied`.
   - `(opRead, authMissing, "public-read")` → nil.
   - `(opRead, authMissing, "" / "private" / anything-else)` → 403 `AccessDenied`.

3. **Handler integration tests.** Each HTTP verb through the full handler with an auth-enabled config:
   - `PUT /bucket/key` unauth → 403 `AccessDenied`.
   - `PUT /bucket/key` wrong key → 403 `InvalidAccessKeyId`.
   - `PUT /bucket/key` right key → 200.
   - `GET /bucket/private-key` unauth → 403.
   - `GET /bucket/public-key` (written authed with `x-amz-acl: public-read`) unauth → 200.
   - `DELETE`, `COPY`, `POST` unauth → 403.

4. **Fallback interaction tests.**
   - `FallbackPublic=false`, unauth GET for missing key → 403 (no fallback served).
   - `FallbackPublic=true`, unauth GET for missing key → 200 + fallback bytes.
   - Either config, authed GET for missing key → 200 + fallback.

5. **Backwards-compat.** With `AccessKey=""`, every existing test in `handler_test.go` and `storage_test.go` must still pass. Those test files are not modified.

**Out of scope for tests:** SigV4 signature validity, clock skew, presigned URLs, SDK-level integration with boto3/aws-sdk-go (one-time smoke verification is fine, not a CI test).

## Backwards Compatibility

With `ESSIE3_ACCESS_KEY` unset (the default), every code path short-circuits on `authNotRequired` and the server behaves exactly as it does today. This is the contract — existing consumers see no change unless they opt in.

`ObjectMeta.ACL` is already stored on disk today. After this change, that stored value starts being *read* on GET/HEAD to decide public-read. Objects written before the change have `ACL == ""`, which is the non-public path — strictly-tighter behavior, no breakage.
