# Storage body/meta atomicity — design

## Goal

Close the body/meta atomicity gap in `storage.go` flagged as critical
finding #1 in the 2026-05-14 project audit. After this change, a
caller of `GetObject` / `HeadObject` cannot observe an object whose
on-disk body bytes disagree with the served `ETag` /
`ContentLength` / `CreatedAt` fields, regardless of:

- concurrent `PutObject` / `DeleteObject` callers on the same key
  inside a single essie3 process, or
- a meta-write failure (disk full, EROFS, EACCES) after a successful
  body-write.

Out of scope: cross-process coordination (multiple essie3 replicas
sharing the same `DATA_DIR`) and recovery from a server crash that
happens between the body-write and the meta-write.

## Architecture

`Storage` gains one private field and one private helper:

```go
type Storage struct {
    dataDir string
    keyMu   sync.Map  // map[string]*sync.RWMutex (key = bucket+"/"+key)
}

// keyMutex returns the per-key RWMutex, lazily creating it on first
// use. Serializes writers against each other AND against readers, so
// a reader cannot observe the brief window between a writer's body
// rename and its meta rename.
func (s *Storage) keyMutex(bucket, key string) *sync.RWMutex
```

Locking matrix:

| Method        | Lock                        |
| ------------- | --------------------------- |
| `PutObject`   | `Lock` / `Unlock` (write)   |
| `DeleteObject`| `Lock` / `Unlock` (write)   |
| `GetObject`   | `RLock` / `RUnlock` (read)  |
| `HeadObject`  | `RLock` / `RUnlock` (read)  |
| `CopyObject`  | none directly — calls `GetObject` (RLock src) then `PutObject` (Lock dst) |
| `BucketExists`, `CreateBucket` | none — bucket-level, not keyed |

Mutex lifetime: `sync.Map.LoadOrStore` lazily creates each per-key
mutex; mutexes are never deleted. Memory grows with the number of
distinct keys ever written or read for the lifetime of the process.
For a dev tool with a bounded test corpus this is acceptable.

## PutObject write path with rollback

Inside the per-key write lock, the new sequence is:

```go
prevBody, prevErr := os.ReadFile(objPath)
if prevErr != nil && !os.IsNotExist(prevErr) {
    return "", fmt.Errorf("read prev body: %w", prevErr)
}
hadPrev := prevErr == nil

// (existing code: build meta, marshal to metaBytes, compute etag)

if err := writeFileAtomic(objPath, body, 0o644); err != nil {
    return "", fmt.Errorf("write object: %w", err)
}

if err := writeFileAtomic(s.metaPath(bucket, key), metaBytes, 0o644); err != nil {
    if rbErr := rollbackBody(objPath, prevBody, hadPrev); rbErr != nil {
        log.Printf("rollback after meta-write failure for %s/%s: %v", bucket, key, rbErr)
    }
    return "", fmt.Errorf("write meta: %w", err)
}

return etag, nil
```

The rollback helper is extracted as a small unit-testable function:

```go
// rollbackBody restores objPath to its prior state after a meta-write
// failure. If hadPrev=true, prevBody is rewritten atomically; if
// hadPrev=false (the body was newly created by this PUT), objPath is
// removed.
func rollbackBody(objPath string, prevBody []byte, hadPrev bool) error
```

If the rollback itself fails (e.g. the same disk-full condition that
broke the meta write), we log it but still return the original
meta-write error. The meta-write error is what the HTTP client cares
about.

## DeleteObject

`DeleteObject` takes the write lock for the duration of both
`os.Remove` calls, so a concurrent reader cannot observe the
body-removed/meta-still-there window. No rollback is needed; the
existing log-on-error behavior on each `os.Remove` is unchanged.

## Memory cost of rollback

`prevBody` doubles peak memory during a `PutObject` that overwrites an
existing object: we hold both the previous bytes and the new bytes.
For a dev tool with mostly-small objects this is a non-concern. For a
large object being overwritten, the cost is real. Flagging — not
optimizing for this case (essie3 is not a production S3 replacement).

## Crash-mid-PUT (out of scope)

If the essie3 process is killed between `writeFileAtomic(body)` and
`writeFileAtomic(meta)`, the in-process rollback never runs. On
restart the body is new and the meta is old. essie3 already handles
SIGINT/SIGTERM with a graceful `srv.Shutdown` (`main.go`), so this
window only opens on `kill -9`, OOM, or a hardware crash — exotic for
a local dev tool.

Documented as a known limitation in the README. Recovery (a startup
scan that re-creates or deletes mismatched pairs) is not implemented.

## Testing

A new file `storage_atomicity_test.go`, separate from
`storage_test.go` to keep the concurrency-heavy tests self-contained.

**Unit tests for `rollbackBody`:**

- `hadPrev=true, prevBody=[]byte("old")` over an existing file → file
  content is restored to `"old"`.
- `hadPrev=false` over an existing file (the new body) → file is
  removed.
- `hadPrev=false` over a non-existent file (PUT failed before any
  write reached disk) → returns nil, file remains absent.

**Concurrency tests for `PutObject` / `DeleteObject` / readers:**

- `TestStorage_ConcurrentPutsAreConsistent` — spawn ~20 goroutines
  each calling `PutObject("b", "k", uniqueBody, &ObjectMeta{...})`.
  After `wg.Wait()`, read back and assert that the served body matches
  one of the writers' inputs verbatim AND
  `meta.ETag == fmt.Sprintf("\"%x\"", md5.Sum(obj.Body))` AND
  `meta.ContentLength == int64(len(obj.Body))`. On the pre-fix code
  this would be flaky/fail; on the fixed code it passes
  deterministically.
- `TestStorage_ConcurrentPutAndGet` — one goroutine puts in a loop
  with rotating bodies; another reads in a loop. Every successful Get
  must produce a body whose `md5.Sum` equals the served `ETag`. Fixed
  iteration count, not wall-clock based, for determinism.
- `TestStorage_ConcurrentDeleteAndGet` — similar, but with deletes.
  Each Get must see either a fully-consistent object or
  `os.ErrNotExist`, never a half state.

These run cleanly under `go test -race -count=1` (CI's invocation).
They are regression guards for the mutex contract, not a proof of
full atomicity.

**No simulated meta-write failure end-to-end test.** Triggering one
needs either platform-specific filesystem-permission tricks or an
injectable writer, both of which over-engineer the change. The
rollback path is covered by the `rollbackBody` unit tests; the
call-site logic is small and reviewable.

## Documentation updates

- **`README.md`**, `## Storage layout`: append to the "Metadata is
  written atomically alongside the body." line — "PUT and DELETE on
  the same key are serialized via an in-process per-key lock so
  concurrent writers cannot leave a body/meta mismatch. essie3 does
  not coordinate across multiple processes sharing the same
  `DATA_DIR`."
- **`CLAUDE.md`**: in the `storage.go` architecture bullet, add a
  one-liner that PUT/DELETE on the same key serialize through a
  per-key `sync.RWMutex` for body/meta consistency.

## File changes summary

- **Modify** `storage.go` — add `keyMu sync.Map` to `Storage`, the
  `keyMutex` helper, the `rollbackBody` helper, and the lock + prev-body
  snapshot + rollback logic in `PutObject`. Add `Lock`/`Unlock` to
  `DeleteObject` and `RLock`/`RUnlock` to `GetObject` /
  `HeadObject`.
- **Create** `storage_atomicity_test.go` — `rollbackBody` unit tests
  plus the three concurrency tests above.
- **Modify** `README.md` — one sentence appended to `## Storage layout`.
- **Modify** `CLAUDE.md` — one bullet edit in the `storage.go`
  architecture line.

## Out of scope

- Cross-process coordination (multiple essie3 replicas sharing the
  same `DATA_DIR`).
- Crash recovery for the body-write-then-killed window.
- Mutex eviction (memory grows with distinct keys touched for the
  process lifetime).
- A simulated meta-write-failure end-to-end test.
