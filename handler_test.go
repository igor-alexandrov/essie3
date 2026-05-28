package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
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
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
	h := NewHandler(s, fb, auth)
	return httptest.NewServer(h)
}

func testServerWithFallback(t *testing.T, fallbackDir string, mode FallbackMode) *httptest.Server {
	t.Helper()
	dataDir := t.TempDir()
	s := NewStorage(dataDir)
	fb, _ := NewFallback(fallbackDir, DefaultInlineExtensions, mode)
	h := NewHandler(s, fb, AuthConfig{})
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

func TestHandler_GetObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/../escape/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml (must not serve fallback bytes)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_HeadObject_InvalidNameReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/../escape/photo.jpg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_CopyObject_InvalidSourceReturns400(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	put, _ := http.NewRequest("PUT", srv.URL+"/b/src.txt", strings.NewReader("hello"))
	http.DefaultClient.Do(put)

	req, _ := http.NewRequest("PUT", srv.URL+"/b/dst.txt", nil)
	req.Header.Set("x-amz-copy-source", "/b/../escape")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>InvalidArgument</Code>") {
		t.Errorf("body missing <Code>InvalidArgument</Code>:\n%s", body)
	}
}

func TestHandler_CopyObject_MissingSourceReturns404(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/b/dst.txt", nil)
	req.Header.Set("x-amz-copy-source", "/b/missing-key.txt")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>NoSuchKey</Code>") {
		t.Errorf("body missing <Code>NoSuchKey</Code>:\n%s", body)
	}
	if !strings.Contains(string(body), "<Key>missing-key.txt</Key>") {
		t.Errorf("body missing <Key>missing-key.txt</Key>:\n%s", body)
	}
}

func TestHandler_Generate_GetPNG(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anybucket/missing/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if ar := resp.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	etag := resp.Header.Get("ETag")
	if len(etag) != 34 || etag[0] != '"' || etag[33] != '"' {
		t.Errorf("ETag = %q, want a quoted 32-hex-char string", etag)
	}
	if lm := resp.Header.Get("Last-Modified"); lm == "" {
		t.Errorf("Last-Modified is empty")
	}

	body, _ := io.ReadAll(resp.Body)
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	if img.Bounds().Dx() != canvasSize || img.Bounds().Dy() != canvasSize {
		t.Errorf("decoded PNG = %dx%d, want %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), canvasSize, canvasSize)
	}
}

func TestHandler_Generate_GetJPEG(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/b/missing/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}
}

func TestHandler_Generate_UnsupportedExtNoSuchKey(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/b/missing/doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404 (pdf is in pool but not generatable, pool disabled)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<Code>NoSuchKey</Code>") {
		t.Errorf("body missing NoSuchKey:\n%s", body)
	}
}

func TestHandler_Generate_HeadEmptyBody(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	req, _ := http.NewRequest("HEAD", srv.URL+"/b/missing/photo.png", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if et := resp.Header.Get("ETag"); et == "" {
		t.Errorf("HEAD missing ETag")
	}
	if lm := resp.Header.Get("Last-Modified"); lm == "" {
		t.Errorf("HEAD missing Last-Modified")
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD returned %d body bytes, want 0", len(body))
	}
}

func TestHandler_Generate_RangeRequest(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	// First fetch the full body to learn its length.
	full, err := http.Get(srv.URL + "/b/r/key.png")
	if err != nil {
		t.Fatal(err)
	}
	fullBody, _ := io.ReadAll(full.Body)
	full.Body.Close()
	total := len(fullBody)
	if total < 200 {
		t.Fatalf("generated body too short for range test: %d bytes", total)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/b/r/key.png", nil)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	wantCR := fmt.Sprintf("bytes 0-99/%d", total)
	if cr := resp.Header.Get("Content-Range"); cr != wantCR {
		t.Errorf("Content-Range = %q, want %q", cr, wantCR)
	}
	sliced, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(sliced, fullBody[:100]) {
		t.Errorf("sliced body does not match first 100 bytes of full body")
	}
}

func TestHandler_Generate_TwoRequestsIdentical(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeGenerate)
	defer srv.Close()

	get := func() ([]byte, string) {
		resp, err := http.Get(srv.URL + "/b/stable/key.png")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return body, resp.Header.Get("ETag")
	}
	a, etagA := get()
	b, etagB := get()
	if !bytes.Equal(a, b) {
		t.Errorf("two GETs on the same key returned different bytes")
	}
	if etagA != etagB {
		t.Errorf("two GETs on the same key returned different ETags: %q vs %q", etagA, etagB)
	}
}

func TestHandler_Both_PoolFirst(t *testing.T) {
	srv := testServerWithFallback(t, "testdata/fallback", FallbackModeBoth)
	defer srv.Close()

	// .pdf is in the pool but not generatable; under both mode the pool
	// match must win.
	resp, err := http.Get(srv.URL + "/b/missing/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Pool placeholders carry no ETag in this codebase.
	if et := resp.Header.Get("ETag"); et != "" {
		t.Errorf("ETag = %q on pool placeholder, want empty (pool placeholders are ETag-less)", et)
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		t.Errorf("Last-Modified = %q on pool placeholder, want empty", lm)
	}
}

func TestHandler_Both_GenerateWhenPoolMisses(t *testing.T) {
	// Empty pool dir → no pool match for any extension; .png falls through
	// to the generator.
	srv := testServerWithFallback(t, t.TempDir(), FallbackModeBoth)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/b/missing/key.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if et := resp.Header.Get("ETag"); et == "" {
		t.Errorf("generated response missing ETag")
	}
}
