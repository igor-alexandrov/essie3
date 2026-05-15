package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
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

	wantETag := fmt.Sprintf("\"%x\"", md5.Sum(obj.Body))
	if obj.Meta.ETag != wantETag {
		t.Errorf("ETag = %q, want %q (body/meta mismatch)", obj.Meta.ETag, wantETag)
	}
	if obj.Meta.ContentLength != int64(len(obj.Body)) {
		t.Errorf("ContentLength = %d, want %d", obj.Meta.ContentLength, len(obj.Body))
	}
}
