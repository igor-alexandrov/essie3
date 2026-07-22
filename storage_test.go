package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPutAndGetObject(t *testing.T) {
	s := NewStorage(t.TempDir())

	body := []byte("hello world")
	meta := &ObjectMeta{
		ContentType: "text/plain",
		ACL:         "public-read",
	}

	etag, err := s.PutObject("mybucket", "mykey.txt", body, meta)
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if etag == "" {
		t.Fatal("expected non-empty etag")
	}

	obj, err := s.GetObject("mybucket", "mykey.txt")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(obj.Body) != "hello world" {
		t.Fatalf("body = %q, want %q", obj.Body, "hello world")
	}
	if obj.Meta.ContentType != "text/plain" {
		t.Fatalf("content_type = %q, want %q", obj.Meta.ContentType, "text/plain")
	}
	if obj.Meta.ETag != etag {
		t.Fatalf("etag = %q, want %q", obj.Meta.ETag, etag)
	}
}

func TestGetObject_NotFound(t *testing.T) {
	s := NewStorage(t.TempDir())

	_, err := s.GetObject("nobucket", "nokey.txt")
	if !os.IsNotExist(err) {
		t.Fatalf("expected os.IsNotExist, got %v", err)
	}
}

func TestPutObject_CreatesIntermediateDirectories(t *testing.T) {
	s := NewStorage(t.TempDir())

	_, err := s.PutObject("bucket", "deep/nested/path/file.txt", []byte("data"), &ObjectMeta{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	obj, err := s.GetObject("bucket", "deep/nested/path/file.txt")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(obj.Body) != "data" {
		t.Fatalf("body = %q, want %q", obj.Body, "data")
	}
}

func TestHeadObject(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("b", "k.txt", []byte("data"), &ObjectMeta{ContentType: "text/plain"})

	meta, err := s.HeadObject("b", "k.txt")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if meta.ContentType != "text/plain" {
		t.Fatalf("content_type = %q", meta.ContentType)
	}
	if meta.ContentLength != 4 {
		t.Fatalf("content_length = %d, want 4", meta.ContentLength)
	}
}

func TestDeleteObject(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("b", "k.txt", []byte("data"), &ObjectMeta{ContentType: "text/plain"})

	s.DeleteObject("b", "k.txt")

	_, err := s.GetObject("b", "k.txt")
	if !os.IsNotExist(err) {
		t.Fatalf("expected not exist after delete, got %v", err)
	}
}

func TestDeleteObject_NonExistent(t *testing.T) {
	s := NewStorage(t.TempDir())
	// Should not panic or error
	s.DeleteObject("b", "nokey.txt")
}

func TestCopyObject(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("b", "src.txt", []byte("copy me"), &ObjectMeta{ContentType: "text/plain"})

	etag, err := s.CopyObject("b", "src.txt", "b", "dst.txt")
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if etag == "" {
		t.Fatal("expected etag")
	}

	obj, err := s.GetObject("b", "dst.txt")
	if err != nil {
		t.Fatalf("GetObject dst: %v", err)
	}
	if string(obj.Body) != "copy me" {
		t.Fatalf("body = %q", obj.Body)
	}
}

func TestPutObject_RejectsPathTraversal(t *testing.T) {
	s := NewStorage(t.TempDir())

	cases := []struct{ bucket, key string }{
		{"../evil", "x"},
		{"b", "../escape"},
		{"b", "sub/../../escape"},
		{"b", "/abs"},
		{"", "k"},
		{"b", ""},
	}
	for _, c := range cases {
		if _, err := s.PutObject(c.bucket, c.key, []byte("x"), &ObjectMeta{}); err == nil {
			t.Errorf("PutObject(%q, %q) = nil err, want rejection", c.bucket, c.key)
		}
	}
}

func TestBucketCreateAndExists(t *testing.T) {
	s := NewStorage(t.TempDir())

	if s.BucketExists("newbucket") {
		t.Fatal("bucket should not exist yet")
	}

	if err := s.CreateBucket("newbucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	if !s.BucketExists("newbucket") {
		t.Fatal("bucket should exist after create")
	}
}

func TestStorage_DeleteObject_InvalidNameReturnsError(t *testing.T) {
	s := NewStorage(t.TempDir())

	if err := s.DeleteObject("..", "k"); !errors.Is(err, errInvalidName) {
		t.Errorf("DeleteObject(..) error = %v, want errInvalidName", err)
	}
	if err := s.DeleteObject("b", "../escape"); !errors.Is(err, errInvalidName) {
		t.Errorf("DeleteObject(b, ../escape) error = %v, want errInvalidName", err)
	}
}

func TestStorage_DeleteObject_MissingFilesReturnsNil(t *testing.T) {
	s := NewStorage(t.TempDir())

	if err := s.DeleteObject("b", "never-existed.txt"); err != nil {
		t.Errorf("DeleteObject on never-existed key = %v, want nil (idempotent)", err)
	}
}

func TestListBuckets(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("beta", "one.txt", []byte("hello"), &ObjectMeta{ContentType: "text/plain"})     // 5
	s.PutObject("beta", "two.txt", []byte("worldwide"), &ObjectMeta{ContentType: "text/plain"}) // 9
	s.PutObject("alpha", "x", []byte("z"), &ObjectMeta{ContentType: "text/plain"})              // 1

	buckets, err := s.ListBuckets()
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(buckets), buckets)
	}
	// Sorted by name: alpha, beta.
	if buckets[0].Name != "alpha" || buckets[1].Name != "beta" {
		t.Fatalf("buckets not sorted: %+v", buckets)
	}
	if buckets[0].ObjectCount != 1 || buckets[0].TotalBytes != 1 {
		t.Errorf("alpha = %+v, want count 1 size 1", buckets[0])
	}
	// Sidecars must not be counted as objects.
	if buckets[1].ObjectCount != 2 {
		t.Errorf("beta count = %d, want 2 (sidecars excluded)", buckets[1].ObjectCount)
	}
	if buckets[1].TotalBytes != 14 {
		t.Errorf("beta size = %d, want 14", buckets[1].TotalBytes)
	}
}

func TestListBuckets_MissingDataDir(t *testing.T) {
	s := NewStorage(filepath.Join(t.TempDir(), "does-not-exist"))
	buckets, err := s.ListBuckets()
	if err != nil {
		t.Fatalf("ListBuckets on missing dataDir = %v, want nil", err)
	}
	if len(buckets) != 0 {
		t.Fatalf("got %d buckets, want 0", len(buckets))
	}
}

func TestListObjects(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("b", "deep/nested/f.txt", []byte("data"), &ObjectMeta{ContentType: "text/plain", ACL: "public-read"})
	s.PutObject("b", "a.txt", []byte("hi"), &ObjectMeta{ContentType: "text/plain"})

	objs, err := s.ListObjects("b")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2: %+v", len(objs), objs)
	}
	// Sorted by key: "a.txt" < "deep/nested/f.txt".
	if objs[0].Key != "a.txt" {
		t.Errorf("objs[0].Key = %q, want a.txt", objs[0].Key)
	}
	if objs[1].Key != "deep/nested/f.txt" {
		t.Errorf("objs[1].Key = %q, want deep/nested/f.txt (forward slashes)", objs[1].Key)
	}
	if objs[1].ContentType != "text/plain" || objs[1].ACL != "public-read" {
		t.Errorf("nested object meta wrong: %+v", objs[1])
	}
	if objs[1].Size != 4 {
		t.Errorf("nested object size = %d, want 4", objs[1].Size)
	}
	if objs[1].CreatedAt.IsZero() {
		t.Errorf("nested object CreatedAt should be set from meta")
	}
}

func TestListObjects_MissingSidecar(t *testing.T) {
	s := NewStorage(t.TempDir())
	// Write a raw body with no .meta.json sidecar.
	dir := filepath.Join(s.dataDir, "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphan.txt"), []byte("orphaned"), 0o644); err != nil {
		t.Fatal(err)
	}

	objs, err := s.ListObjects("b")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("got %d objects, want 1", len(objs))
	}
	o := objs[0]
	if o.Key != "orphan.txt" {
		t.Errorf("key = %q, want orphan.txt", o.Key)
	}
	if o.Size != 8 {
		t.Errorf("size = %d, want 8 (on-disk fallback)", o.Size)
	}
	if !o.CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want zero for missing meta", o.CreatedAt)
	}
}

func TestListObjects_UnknownBucketAndInvalidName(t *testing.T) {
	s := NewStorage(t.TempDir())

	if _, err := s.ListObjects("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ListObjects(nope) err = %v, want os.ErrNotExist", err)
	}
	if _, err := s.ListObjects(".."); !errors.Is(err, errInvalidName) {
		t.Errorf("ListObjects(..) err = %v, want errInvalidName", err)
	}
}
