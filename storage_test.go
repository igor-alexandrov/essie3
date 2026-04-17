package main

import (
	"os"
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
