package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteInvalidRange(t *testing.T) {
	rec := httptest.NewRecorder()

	writeInvalidRange(rec, "mybucket", "photos/photo.jpg", 1000)

	if rec.Code != 416 {
		t.Errorf("status = %d, want 416", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */1000" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes */1000")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want %q", got, "application/xml")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<Code>InvalidRange</Code>") {
		t.Errorf("body missing <Code>InvalidRange</Code>:\n%s", body)
	}
	if !strings.Contains(body, "<BucketName>mybucket</BucketName>") {
		t.Errorf("body missing <BucketName>mybucket</BucketName>:\n%s", body)
	}
	if !strings.Contains(body, "<Key>photos/photo.jpg</Key>") {
		t.Errorf("body missing <Key>photos/photo.jpg</Key>:\n%s", body)
	}
}
