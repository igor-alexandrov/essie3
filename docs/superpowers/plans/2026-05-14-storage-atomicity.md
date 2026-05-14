# Storage Body/Meta Atomicity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the body/meta atomicity gap in `storage.go` (audit critical finding #1) so a `GetObject`/`HeadObject` caller cannot observe an object whose on-disk body bytes disagree with the served `ETag`/`ContentLength`/`CreatedAt`, regardless of concurrent in-process writers or a meta-write failure after a successful body-write.

**Architecture:** `Storage` gains a `sync.Map` of per-key `*sync.RWMutex`. `PutObject`/`DeleteObject` take the write lock; `GetObject`/`HeadObject` take the read lock. `PutObject` snapshots the previous body in memory before writing, then on meta-write failure rolls the body back via a small extracted `rollbackBody` helper. Storage on-disk format is unchanged.

**Tech Stack:** Go 1.22, standard library only (`sync`, `os`).

**Spec:** [`docs/superpowers/specs/2026-05-14-storage-atomicity-design.md`](../specs/2026-05-14-storage-atomicity-design.md)

---

## File Structure

- **Modify** `storage.go` — add `keyMu sync.Map` field to `Storage`, add `keyMutex` method, add `rollbackBody` helper, update `PutObject`/`DeleteObject` (`Lock`/`Unlock`) and `GetObject`/`HeadObject` (`RLock`/`RUnlock`); add `"sync"` to the import block.
- **Create** `storage_atomicity_test.go` — unit tests for `keyMutex` and `rollbackBody`, plus three concurrency tests covering put/put, put/get, delete/get.
- **Modify** `README.md` — append one sentence to `## Storage layout` clarifying the new in-process pair-atomicity guarantee.
- **Modify** `CLAUDE.md` — one bullet edit in the `storage.go` architecture line.

---

### Task 1: `keyMutex` helper

`Storage` gains a `sync.Map` field and a `keyMutex(bucket, key)` accessor that lazily creates a per-key `*sync.RWMutex`. No callers wired up yet — pure plumbing.

**Files:**
- Modify: `storage.go`
- Create: `storage_atomicity_test.go`

- [ ] **Step 1: Write the failing tests**

Create `storage_atomicity_test.go`:

```go
package main

import (
	"sync"
	"testing"
)

func TestStorage_KeyMutex_SameKeyReturnsSamePointer(t *testing.T) {
	s := NewStorage(t.TempDir())
	a := s.keyMutex("b", "k")
	b := s.keyMutex("b", "k")
	if a != b {
		t.Errorf("keyMutex returned different pointers for same (bucket,key)")
	}
}

func TestStorage_KeyMutex_DifferentKeysReturnDifferentPointers(t *testing.T) {
	s := NewStorage(t.TempDir())
	a := s.keyMutex("b", "k1")
	b := s.keyMutex("b", "k2")
	if a == b {
		t.Errorf("keyMutex returned same pointer for different keys")
	}
}

func TestStorage_KeyMutex_ConcurrentLoadOrStoreReturnsSame(t *testing.T) {
	s := NewStorage(t.TempDir())
	const goroutines = 100
	var wg sync.WaitGroup
	pointers := make([]*sync.RWMutex, goroutines)
	for i := range pointers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pointers[i] = s.keyMutex("b", "k")
		}(i)
	}
	wg.Wait()
	for i := 1; i < goroutines; i++ {
		if pointers[i] != pointers[0] {
			t.Errorf("goroutine %d got different pointer than goroutine 0", i)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestStorage_KeyMutex -v`
Expected: compile failure — `s.keyMutex undefined (type *Storage has no field or method keyMutex)`.

- [ ] **Step 3: Add `keyMu` field and `keyMutex` method**

In `storage.go`, add `"sync"` to the import block (alphabetically — between `"strings"` and `"time"`):

```go
import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)
```

Modify the `Storage` struct (around line 29) to add the new field:

```go
type Storage struct {
	dataDir string
	keyMu   sync.Map // map[string]*sync.RWMutex (key = bucket+"/"+key)
}
```

(`NewStorage` does not need a change — `sync.Map`'s zero value is ready to use.)

After the `metaPath` method (around line 60), add:

```go
// keyMutex returns the per-key RWMutex, lazily creating it on first
// use. Used by PutObject/DeleteObject (write lock) and GetObject/
// HeadObject (read lock) to serialize writers vs writers and readers
// vs writers, so a reader cannot observe the brief window between a
// writer's body rename and its meta rename.
func (s *Storage) keyMutex(bucket, key string) *sync.RWMutex {
	k := bucket + "/" + key
	if mu, ok := s.keyMu.Load(k); ok {
		return mu.(*sync.RWMutex)
	}
	mu, _ := s.keyMu.LoadOrStore(k, &sync.RWMutex{})
	return mu.(*sync.RWMutex)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestStorage_KeyMutex -v -race`
Expected: PASS for all three subtests, including under `-race`.

- [ ] **Step 5: Run the full suite to confirm no regression**

Run: `cd /Users/igor/workspace/essie3 && go test ./...`
Expected: all existing tests pass. The field is unused so far; no behavior change.

- [ ] **Step 6: Commit**

```bash
git add storage.go storage_atomicity_test.go
git commit -m "Add per-key RWMutex helper to Storage"
```

---

### Task 2: `rollbackBody` helper

Extract `rollbackBody` as a standalone, unit-testable function. Restores `objPath` to its prior state: rewrites `prevBody` if there was one, deletes the file otherwise. Not yet called from anywhere — Task 3 wires it into `PutObject`.

**Files:**
- Modify: `storage.go`
- Modify: `storage_atomicity_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `storage_atomicity_test.go`:

```go
func TestRollbackBody_RestoresPrevBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "obj")
	if err := os.WriteFile(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rollbackBody(p, []byte("old"), true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "old" {
		t.Errorf("body = %q, want %q", got, "old")
	}
}

func TestRollbackBody_RemovesNewlyCreatedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "obj")
	if err := os.WriteFile(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rollbackBody(p, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file still exists, want IsNotExist; got err=%v", err)
	}
}

func TestRollbackBody_NonExistentFileWithNoPrev(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "absent")
	if err := rollbackBody(p, nil, false); err != nil {
		t.Errorf("unexpected error for absent file: %v", err)
	}
}
```

Add `"os"` and `"path/filepath"` to the test file's import block:

```go
import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestRollbackBody -v`
Expected: compile failure — `undefined: rollbackBody`.

- [ ] **Step 3: Add `rollbackBody` to `storage.go`**

Append to `storage.go` (after `writeFileAtomic`):

```go
// rollbackBody restores objPath to its prior state after a meta-write
// failure. If hadPrev=true, prevBody is rewritten atomically; if
// hadPrev=false (the body was newly created by this PUT), objPath is
// removed. A non-existent file with hadPrev=false is treated as
// already-rolled-back (returns nil).
func rollbackBody(objPath string, prevBody []byte, hadPrev bool) error {
	if hadPrev {
		return writeFileAtomic(objPath, prevBody, 0o644)
	}
	if err := os.Remove(objPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestRollbackBody -v`
Expected: PASS for all three subtests.

- [ ] **Step 5: Commit**

```bash
git add storage.go storage_atomicity_test.go
git commit -m "Add rollbackBody helper for PutObject meta-write failures"
```

---

### Task 3: Wire `PutObject` with mutex + prev-body snapshot + rollback

Replace `PutObject`'s body with the new locked, snapshot-and-rollback version. The mutex eliminates the concurrent-writer tear; the snapshot+rollback handles meta-write failure. Add a concurrent-puts integration test that asserts body/meta consistency.

**Files:**
- Modify: `storage.go`
- Modify: `storage_atomicity_test.go`

- [ ] **Step 1: Write the failing test**

Append to `storage_atomicity_test.go`:

```go
func TestStorage_ConcurrentPutsAreConsistent(t *testing.T) {
	s := NewStorage(t.TempDir())
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	const writers = 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf("writer-%02d", i))
			meta := &ObjectMeta{ContentType: "text/plain"}
			if _, err := s.PutObject("b", "k", body, meta); err != nil {
				t.Errorf("PutObject(writer %d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	obj, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}

	// Body must match one of the writers' inputs verbatim.
	var matched bool
	for i := 0; i < writers; i++ {
		if string(obj.Body) == fmt.Sprintf("writer-%02d", i) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("body = %q, doesn't match any writer's input", obj.Body)
	}

	// Meta ETag must match the actual body bytes.
	wantETag := fmt.Sprintf("\"%x\"", md5.Sum(obj.Body))
	if obj.Meta.ETag != wantETag {
		t.Errorf("ETag = %q, want %q (body/meta mismatch)", obj.Meta.ETag, wantETag)
	}
	if obj.Meta.ContentLength != int64(len(obj.Body)) {
		t.Errorf("ContentLength = %d, want %d", obj.Meta.ContentLength, len(obj.Body))
	}
}
```

Add `"crypto/md5"` and `"fmt"` to the test file's import block:

```go
import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails (under -race)**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestStorage_ConcurrentPutsAreConsistent -race -count=5 -v`
Expected: FAIL or `-race` data race report on the current code (the body/meta tear; multiple `os.Rename` calls racing on body and meta files). The `-count=5` and `-race` together make the failure reliable; a single run might pass by luck.

- [ ] **Step 3: Replace `PutObject` body**

In `storage.go`, replace the entire `PutObject` method with:

```go
func (s *Storage) PutObject(bucket, key string, body []byte, meta *ObjectMeta) (string, error) {
	if err := validateName(bucket); err != nil {
		return "", err
	}
	if err := validateName(key); err != nil {
		return "", err
	}

	mu := s.keyMutex(bucket, key)
	mu.Lock()
	defer mu.Unlock()

	objPath := s.objectPath(bucket, key)

	if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	prevBody, prevErr := os.ReadFile(objPath)
	if prevErr != nil && !os.IsNotExist(prevErr) {
		return "", fmt.Errorf("read prev body: %w", prevErr)
	}
	hadPrev := prevErr == nil

	etag := fmt.Sprintf("\"%x\"", md5.Sum(body))
	meta.ETag = etag
	meta.ContentLength = int64(len(body))
	meta.CreatedAt = time.Now().UTC()

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal meta: %w", err)
	}

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
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run TestStorage_ConcurrentPutsAreConsistent -race -count=10 -v`
Expected: PASS, no race reports across all 10 invocations.

- [ ] **Step 5: Run the full suite to confirm no regression**

Run: `cd /Users/igor/workspace/essie3 && go test -race -count=1 ./...`
Expected: all tests pass. Existing single-threaded `storage_test.go` and `handler_test.go` tests are unaffected by the new lock.

- [ ] **Step 6: Commit**

```bash
git add storage.go storage_atomicity_test.go
git commit -m "Lock PutObject and rollback body on meta-write failure"
```

---

### Task 4: Wire `DeleteObject`, `GetObject`, `HeadObject` with mutex

Reads need to take the per-key read lock so they can't observe a writer's mid-flight state; `DeleteObject` needs the write lock to serialize against `PutObject` and to keep the body/meta removal pair atomic from a reader's perspective. Add two more concurrency tests: put/get and delete/get.

**Files:**
- Modify: `storage.go`
- Modify: `storage_atomicity_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `storage_atomicity_test.go`:

```go
func TestStorage_ConcurrentPutAndGet(t *testing.T) {
	s := NewStorage(t.TempDir())
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	// Seed so the reader doesn't race against an empty key.
	if _, err := s.PutObject("b", "k", []byte("seed"), &ObjectMeta{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}

	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			body := []byte(fmt.Sprintf("body-%03d", i))
			if _, err := s.PutObject("b", "k", body, &ObjectMeta{ContentType: "text/plain"}); err != nil {
				t.Errorf("PutObject: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			obj, err := s.GetObject("b", "k")
			if err != nil {
				t.Errorf("GetObject: %v", err)
				return
			}
			wantETag := fmt.Sprintf("\"%x\"", md5.Sum(obj.Body))
			if obj.Meta.ETag != wantETag {
				t.Errorf("iter %d: ETag = %q, want %q (body/meta mismatch)", i, obj.Meta.ETag, wantETag)
				return
			}
		}
	}()

	wg.Wait()
}

func TestStorage_ConcurrentDeleteAndGet(t *testing.T) {
	s := NewStorage(t.TempDir())
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)

	// Putter loops put-then-delete to keep the writer busy.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			body := []byte(fmt.Sprintf("body-%03d", i))
			if _, err := s.PutObject("b", "k", body, &ObjectMeta{ContentType: "text/plain"}); err != nil {
				t.Errorf("PutObject: %v", err)
				return
			}
			s.DeleteObject("b", "k")
		}
	}()

	// Reader either sees a fully-consistent object or os.ErrNotExist.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			obj, err := s.GetObject("b", "k")
			if err != nil {
				if !os.IsNotExist(err) {
					t.Errorf("GetObject: unexpected err %v", err)
					return
				}
				continue
			}
			wantETag := fmt.Sprintf("\"%x\"", md5.Sum(obj.Body))
			if obj.Meta.ETag != wantETag {
				t.Errorf("iter %d: ETag = %q, want %q (body/meta mismatch)", i, obj.Meta.ETag, wantETag)
				return
			}
		}
	}()

	wg.Wait()
}
```

(All needed imports — `crypto/md5`, `fmt`, `os`, `sync`, `testing` — are already in the test file from earlier tasks.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestStorage_Concurrent(PutAndGet|DeleteAndGet)" -race -count=3 -v`
Expected: FAIL or `-race` data race report on the current code (`Get` reads body and meta without coordination against `Put`/`Delete`'s renames).

- [ ] **Step 3: Modify `GetObject`, `HeadObject`, `DeleteObject` to take the lock**

In `storage.go`, replace `GetObject`:

```go
func (s *Storage) GetObject(bucket, key string) (*StoredObject, error) {
	if err := validateName(bucket); err != nil {
		return nil, err
	}
	if err := validateName(key); err != nil {
		return nil, err
	}

	mu := s.keyMutex(bucket, key)
	mu.RLock()
	defer mu.RUnlock()

	body, err := os.ReadFile(s.objectPath(bucket, key))
	if err != nil {
		return nil, err
	}

	meta, err := s.readMeta(bucket, key)
	if err != nil {
		return nil, err
	}

	return &StoredObject{Body: body, Meta: meta}, nil
}
```

Replace `HeadObject`:

```go
func (s *Storage) HeadObject(bucket, key string) (*ObjectMeta, error) {
	if err := validateName(bucket); err != nil {
		return nil, err
	}
	if err := validateName(key); err != nil {
		return nil, err
	}
	mu := s.keyMutex(bucket, key)
	mu.RLock()
	defer mu.RUnlock()
	return s.readMeta(bucket, key)
}
```

Replace `DeleteObject`:

```go
func (s *Storage) DeleteObject(bucket, key string) {
	if err := validateName(bucket); err != nil {
		return
	}
	if err := validateName(key); err != nil {
		return
	}
	mu := s.keyMutex(bucket, key)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(s.objectPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete object %s/%s: %v", bucket, key, err)
	}
	if err := os.Remove(s.metaPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete meta %s/%s: %v", bucket, key, err)
	}
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `cd /Users/igor/workspace/essie3 && go test ./... -run "TestStorage_Concurrent(PutAndGet|DeleteAndGet)" -race -count=10 -v`
Expected: PASS, no race reports across all 10 invocations of each.

- [ ] **Step 5: Run the full suite under `-race -count=1` (CI's invocation)**

Run: `cd /Users/igor/workspace/essie3 && go test -race -count=1 ./...`
Expected: all tests pass with no race reports.

- [ ] **Step 6: Commit**

```bash
git add storage.go storage_atomicity_test.go
git commit -m "Lock GetObject, HeadObject, DeleteObject for read/write consistency"
```

---

### Task 5: Documentation updates

Reflect the new in-process pair-atomicity guarantee in the user-facing docs.

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update `README.md`**

In `README.md`, find the line "Metadata is written atomically alongside the body." (in the `## Storage layout` section). Replace just that one line with:

```
Metadata is written atomically alongside the body. PUT and DELETE on
the same key are serialized via an in-process per-key lock so
concurrent writers cannot leave a body/meta mismatch. essie3 does not
coordinate across multiple processes sharing the same `DATA_DIR`.
```

- [ ] **Step 2: Update `CLAUDE.md`**

In `CLAUDE.md`, find the bullet beginning with `**`storage.go`**` in the Architecture section. The current bullet ends with "is the path-traversal defense." Append after that, in the same bullet:

```
PUT/DELETE on the same key serialize through a per-key `sync.RWMutex`; reads take the read lock so they cannot observe a writer's mid-flight body/meta state.
```

- [ ] **Step 3: Verify markdown still renders cleanly**

Run: `cd /Users/igor/workspace/essie3 && grep -n 'in-process per-key lock' README.md && grep -n 'sync.RWMutex' CLAUDE.md`
Expected: each grep returns one match.

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "Document in-process per-key lock for body/meta consistency"
```

---

## Self-Review Notes

**Spec coverage:**
- `keyMu sync.Map` field on `Storage`, `keyMutex` lazy-creating helper → Task 1.
- Per-key RWMutex shape (writers `Lock`, readers `RLock`) → Tasks 1, 3, 4.
- `rollbackBody` extracted helper with the three-case behavior (rewrite prev / remove new / no-op on absent) → Task 2.
- `PutObject` snapshot+lock+rollback sequence → Task 3.
- `DeleteObject` lock + serialized body+meta removal → Task 4.
- `GetObject`/`HeadObject` read lock → Task 4.
- `CopyObject` not modified (calls `GetObject` then `PutObject`, each takes its own lock) → no task needed.
- Concurrent-puts test → Task 3.
- Concurrent put/get test → Task 4.
- Concurrent delete/get test → Task 4.
- `rollbackBody` unit tests → Task 2.
- `keyMutex` unit tests including concurrent first-touch → Task 1.
- README + CLAUDE.md doc updates → Task 5.

**Out of scope (confirmed not in plan):** mutex eviction, cross-process coordination, crash-mid-PUT recovery, simulated meta-write-failure end-to-end test, on-disk format changes.
