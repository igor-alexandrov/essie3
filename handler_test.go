package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	return testServerWithAuth(t, AuthConfig{})
}

func testServerWithAuth(t *testing.T, auth AuthConfig) *httptest.Server {
	t.Helper()
	dataDir := t.TempDir()
	s := NewStorage(dataDir)
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions)
	h := NewHandler(s, fb, auth)
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

func TestHandler_AcceptRangesOnGet(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	resp, err := http.Get(srv.URL + "/b/k.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}

func TestHandler_AcceptRangesOnHead(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("HEAD", srv.URL+"/b/k.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}

func TestHandler_GetWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-4/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-4/11")
	}
	if got := resp.Header.Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q, want %q", got, "5")
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestHandler_GetWithSuffixRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 6-10/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 6-10/11")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("body = %q, want %q", body, "world")
	}
}

func TestHandler_GetWithOpenEndedRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=6-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 6-10/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 6-10/11")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("body = %q, want %q", body, "world")
	}
}

func TestHandler_GetUnsatisfiableRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=1000-2000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 416 {
		t.Fatalf("status = %d, want 416", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes */11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes */11")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidRange</Code>") {
		t.Errorf("body missing InvalidRange code:\n%s", body)
	}
}

func TestHandler_HeadWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("HEAD", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-4/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-4/11")
	}
	if got := resp.Header.Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q, want %q", got, "5")
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %q", body)
	}
}

func TestHandler_IfRangeMatches(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	putResp, _ := http.DefaultClient.Do(req)
	etag := putResp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("PUT did not return ETag")
	}

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("If-Range", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206 (If-Range matched)", resp.StatusCode)
	}
}

func TestHandler_IfRangeMismatch(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/k.txt", strings.NewReader("hello world"))
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/b/k.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("If-Range", `"wrong-etag"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (If-Range mismatched)", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "" {
		t.Errorf("Content-Range = %q, want empty (full response)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want full body", body)
	}
}

func TestHandler_GetFallbackWithRange(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	fullResp, err := http.Get(srv.URL + "/bucket/missing/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	fullBody, _ := io.ReadAll(fullResp.Body)
	fullResp.Body.Close()
	if len(fullBody) < 10 {
		t.Fatalf("fallback body too short (%d bytes) for this test", len(fullBody))
	}

	req, _ := http.NewRequest("GET", srv.URL+"/bucket/missing/photo.jpg", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	wantCR := fmt.Sprintf("bytes 0-9/%d", len(fullBody))
	if got := resp.Header.Get("Content-Range"); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, fullBody[0:10]) {
		t.Errorf("body slice mismatch")
	}
}

func TestHandler_HeadFallbackHasAcceptRanges(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/bucket/missing/photo.jpg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
}

func TestHandler_DeleteObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/b/../escape", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (must not silently 204)", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_DeleteObject_MissingKeyReturns204(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	put, _ := http.NewRequest("PUT", srv.URL+"/b", nil)
	http.DefaultClient.Do(put)

	req, _ := http.NewRequest("DELETE", srv.URL+"/b/never-existed.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (idempotent DELETE)", resp.StatusCode)
	}
}
