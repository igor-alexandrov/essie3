package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testAdminServer(t *testing.T) (*httptest.Server, *TrafficBroker) {
	t.Helper()
	s := NewStorage(t.TempDir())
	s.PutObject("assets", "logo.png", []byte("x"), &ObjectMeta{ContentType: "image/png"})
	s.PutObject("assets", "a<b&c.txt", []byte("yy"), &ObjectMeta{ContentType: "text/plain"})
	b := NewTrafficBroker(50)
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
	admin := NewAdminServer(s, fb, b, time.Now(), "9000", false)
	srv := httptest.NewServer(admin.Handler())
	t.Cleanup(srv.Close)
	return srv, b
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	resp.Body.Close()
	return resp, string(body)
}

func TestAdmin_Index(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// The dashboard lists buckets as links to their own pages; it does
	// not list individual objects.
	for _, want := range []string{"Fallback hit rate", "assets", `href="/buckets/assets"`, "EventSource", "Live traffic"} {
		if !strings.Contains(body, want) {
			t.Errorf("index body missing %q", want)
		}
	}
	// Feed-link config is wired into the page (S3 port + auth state).
	for _, want := range []string{"s3Port", `"9000"`, "authEnabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("index config missing %q", want)
		}
	}
	if strings.Contains(body, "logo.png") {
		t.Error("dashboard should not list objects (logo.png present)")
	}
}

func TestAdmin_BucketPage(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/buckets/assets")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	for _, want := range []string{"logo.png", `href="/"`, `bucket: "assets"`, "EventSource"} {
		if !strings.Contains(body, want) {
			t.Errorf("bucket page missing %q", want)
		}
	}
	// Object keys link to the file on the S3 server (port 9000), new tab.
	if !strings.Contains(body, `:9000/assets/logo.png" target="_blank"`) {
		t.Errorf("bucket page missing object view-link; body:\n%s", body)
	}
	// Keys are HTML-escaped.
	if strings.Contains(body, "a<b&c.txt") {
		t.Error("raw unescaped key present in bucket page")
	}
	if !strings.Contains(body, "a&lt;b&amp;c.txt") {
		t.Error("expected HTML-escaped key a&lt;b&amp;c.txt in bucket page")
	}
}

func TestAdmin_BucketPage_AuthOnlyLinksPublicRead(t *testing.T) {
	s := NewStorage(t.TempDir())
	s.PutObject("b", "pub.txt", []byte("x"), &ObjectMeta{ContentType: "text/plain", ACL: "public-read"})
	s.PutObject("b", "priv.txt", []byte("x"), &ObjectMeta{ContentType: "text/plain"})
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
	// Auth enabled: only public-read objects should be linked.
	admin := NewAdminServer(s, fb, NewTrafficBroker(10), time.Now(), "9000", true)
	srv := httptest.NewServer(admin.Handler())
	defer srv.Close()

	_, body := get(t, srv.URL+"/buckets/b")

	if !strings.Contains(body, `pub.txt</a>`) {
		t.Error("public-read object should be linked under auth")
	}
	if strings.Contains(body, `priv.txt</a>`) {
		t.Error("non-public object must NOT be linked under auth")
	}
	if !strings.Contains(body, `priv.txt</span>`) {
		t.Error("non-public object should render as plain text with a note")
	}
}

func TestAdmin_BucketPage_Unknown(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/buckets/nope")
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if strings.Contains(body, "<Error>") {
		t.Error("bucket 404 should be HTML, not the S3 XML error shape")
	}
}

func TestAdmin_Overview(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/overview")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// /overview returns only the server-derived stat blocks (uptime is a
	// client-side clock, so it is not in this fragment).
	for _, want := range []string{`class="stat"`, "Fallback hit rate", "Buckets", "Objects"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview body missing %q", want)
		}
	}
	for _, forbidden := range []string{"<!DOCTYPE", "<html", "EventSource", "<h2>Buckets</h2>", "Live traffic", "Uptime"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("overview body should not contain %q", forbidden)
		}
	}
}

func TestAdmin_Fragment(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/fragment")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// The dashboard fragment is now the buckets section only.
	for _, want := range []string{"<h2>Buckets</h2>", "assets", `href="/buckets/assets"`} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment body missing %q", want)
		}
	}
	// No page chrome, no feed script, and not the overview stats.
	for _, forbidden := range []string{"<!DOCTYPE", "<html", "EventSource", "Live traffic", "Fallback hit rate"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("fragment body should not contain %q", forbidden)
		}
	}
}

func TestAdmin_BucketFragment(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/buckets/assets/fragment")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "logo.png") {
		t.Error("bucket fragment missing object key logo.png")
	}
	for _, forbidden := range []string{"<!DOCTYPE", "<html", "EventSource"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("bucket fragment should not contain %q", forbidden)
		}
	}
}

func TestAdmin_FragmentMatchesPageRegion(t *testing.T) {
	srv, _ := testAdminServer(t)

	_, page := get(t, srv.URL+"/")
	_, frag := get(t, srv.URL+"/fragment")

	// Compare the drift-stable part (from the Buckets heading on); the
	// stats row above it carries a live uptime that ticks between calls.
	const marker = "<h2>Buckets</h2>"
	i := strings.Index(frag, marker)
	if i < 0 {
		t.Fatal("fragment missing Buckets marker")
	}
	region := frag[i:]
	if !strings.Contains(page, region) {
		t.Error("fragment buckets region does not appear verbatim in the page (template drift)")
	}
}

func TestAdmin_Events(t *testing.T) {
	srv, b := testAdminServer(t)
	b.Publish(TrafficEvent{Method: "GET", Bucket: "assets", Key: "logo.png", Status: 200, Outcome: "real"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf) // backlog is flushed immediately
	cancel()
	got := string(buf[:n])

	for _, want := range []string{"id:", "data:", "logo.png", `"outcome":"real"`} {
		if !strings.Contains(got, want) {
			t.Errorf("events stream missing %q; got:\n%s", want, got)
		}
	}
}

func TestAdmin_CSS(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, body := get(t, srv.URL+"/assets/pico.classless.min.css")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	if len(body) == 0 {
		t.Error("empty CSS body")
	}
}

func TestAdmin_UnknownPathAndMethod(t *testing.T) {
	srv, _ := testAdminServer(t)

	resp, _ := get(t, srv.URL+"/nope")
	if resp.StatusCode != 404 {
		t.Errorf("GET /nope status = %d, want 404", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/fragment", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 405 {
		t.Errorf("POST /fragment status = %d, want 405", resp2.StatusCode)
	}
}
