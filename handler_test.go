package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	dataDir := t.TempDir()
	s := NewStorage(dataDir)
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions)
	h := NewHandler(s, fb)
	return httptest.NewServer(h)
}

func TestHandler_CreateAndHeadBucket(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create bucket
	req, _ := http.NewRequest("PUT", srv.URL+"/mybucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("CreateBucket status = %d", resp.StatusCode)
	}

	// Head bucket
	req, _ = http.NewRequest("HEAD", srv.URL+"/mybucket", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("HeadBucket status = %d", resp.StatusCode)
	}

	// Head non-existent bucket
	req, _ = http.NewRequest("HEAD", srv.URL+"/nonexistent", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("HeadBucket nonexistent status = %d", resp.StatusCode)
	}
}

func TestHandler_PutAndGetObject(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	body := strings.NewReader("hello world")
	req, _ := http.NewRequest("PUT", srv.URL+"/bucket/myfile.txt", body)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("PutObject status = %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header")
	}

	resp, err = http.Get(srv.URL + "/bucket/myfile.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GetObject status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "hello world" {
		t.Fatalf("body = %q", data)
	}
	if resp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestHandler_HeadObject(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("data"))
	req.Header.Set("Content-Type", "text/plain")
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("HEAD", srv.URL+"/b/k.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("expected ETag")
	}
	if resp.Header.Get("Content-Length") != "4" {
		t.Fatalf("content-length = %q", resp.Header.Get("Content-Length"))
	}
}

func TestHandler_DeleteObject(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("data"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("DELETE", srv.URL+"/b/k.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	// After delete, GET returns 404 (no .txt placeholders)
	resp, err = http.Get(srv.URL + "/b/k.txt")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

func TestHandler_CopyObject(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/src.txt", strings.NewReader("copy me"))
	req.Header.Set("Content-Type", "text/plain")
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("PUT", srv.URL+"/b/dst.txt", nil)
	req.Header.Set("x-amz-copy-source", "/b/src.txt")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("copy status = %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/b/dst.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "copy me" {
		t.Fatalf("copied body = %q", data)
	}
}

func TestHandler_PostObject(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/mybucket", nil)
	http.DefaultClient.Do(req)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("key", "uploads/photo.jpg")
	part, _ := writer.CreateFormFile("file", "photo.jpg")
	part.Write([]byte("fake image data"))
	writer.Close()

	req, _ = http.NewRequest("POST", srv.URL+"/mybucket", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("PostObject status = %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("expected ETag header")
	}

	resp, err = http.Get(srv.URL + "/mybucket/uploads/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "fake image data" {
		t.Fatalf("body = %q", data)
	}
}

func TestHandler_GetObject_FallbackImage(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/bucket/missing/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 fallback, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		t.Fatalf("content-type = %q, expected image/*", resp.Header.Get("Content-Type"))
	}
	cd := resp.Header.Get("Content-Disposition")
	want := `inline; filename="photo.jpg"`
	if cd != want {
		t.Fatalf("Content-Disposition = %q, want %q", cd, want)
	}
}

func TestHandler_HeadObject_FallbackImage(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/bucket/missing/photo.jpg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 fallback for HEAD, got %d", resp.StatusCode)
	}
	cd := resp.Header.Get("Content-Disposition")
	want := `inline; filename="photo.jpg"`
	if cd != want {
		t.Fatalf("Content-Disposition = %q, want %q", cd, want)
	}
}

func TestHandler_GetObject_FallbackForAnyMissingKey(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/bucket/missing/doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 fallback for missing PDF, got %d", resp.StatusCode)
	}
}

func TestHandler_CORS(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/bucket/key", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("OPTIONS status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("CORS origin = %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}

	resp, err = http.Get(srv.URL + "/bucket/missing.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS on regular response")
	}
}
