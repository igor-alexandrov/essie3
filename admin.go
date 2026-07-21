package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

//go:embed admin.html.tmpl
var adminTemplate string

//go:embed pico.classless.min.css
var adminCSS []byte

// AdminServer serves the read-only dashboard: a single page plus an SSE
// traffic stream and a soft-refresh HTML fragment.
type AdminServer struct {
	storage   *Storage
	fallback  *Fallback
	broker    *TrafficBroker
	startedAt time.Time
	s3Port    string // ESSIE3_PORT — where object view-links point
	tmpl      *template.Template
}

// NewAdminServer parses the embedded template once and returns a ready
// server. s3Port is the S3 API port; object view-links on bucket pages
// point at http://<admin host>:<s3Port>/<bucket>/<key>.
func NewAdminServer(s *Storage, fb *Fallback, b *TrafficBroker, startedAt time.Time, s3Port string) *AdminServer {
	return &AdminServer{
		storage:   s,
		fallback:  fb,
		broker:    b,
		startedAt: startedAt,
		s3Port:    s3Port,
		tmpl:      template.Must(template.New("admin").Parse(adminTemplate)),
	}
}

// Handler wires the read-only routes. Method+path patterns (Go 1.22)
// give unknown paths a 404 and wrong methods a 405 for free.
func (a *AdminServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /overview", a.handleOverview)
	mux.HandleFunc("GET /fragment", a.handleFragment)
	mux.HandleFunc("GET /buckets/{name}", a.handleBucket)
	mux.HandleFunc("GET /buckets/{name}/fragment", a.handleBucketFragment)
	mux.HandleFunc("GET /events", a.handleEvents)
	mux.HandleFunc("GET /assets/pico.classless.min.css", a.handleCSS)
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	return mux
}

// --- view models ---

type dashboardView struct {
	Uptime      string
	StartedMs   int64 // process start, Unix ms — the JS uptime clock's origin
	BucketCount int
	ObjectCount int
	TotalBytes  string
	HitRate     string
	Buckets     []dashboardBucket
}

type dashboardBucket struct {
	Name        string
	Path        string // link to the bucket's standalone page
	ObjectCount int
	TotalBytes  string
}

type bucketView struct {
	Name    string
	Objects []adminObject
}

type adminObject struct {
	Key         string
	URL         string // absolute link to the object on the S3 server
	Size        string
	ContentType string
	ACL         string
	Created     string
}

func (a *AdminServer) dashboard() dashboardView {
	reads, fallbacks := a.broker.Stats()
	hitRate := "n/a"
	if reads > 0 {
		hitRate = fmt.Sprintf("%.0f%%", float64(fallbacks)/float64(reads)*100)
	}

	buckets, _ := a.storage.ListBuckets()
	vbuckets := make([]dashboardBucket, 0, len(buckets))
	var totalObjects int
	var totalBytes int64
	for _, b := range buckets {
		vbuckets = append(vbuckets, dashboardBucket{
			Name:        b.Name,
			Path:        "/buckets/" + url.PathEscape(b.Name),
			ObjectCount: b.ObjectCount,
			TotalBytes:  humanBytes(b.TotalBytes),
		})
		totalObjects += b.ObjectCount
		totalBytes += b.TotalBytes
	}

	return dashboardView{
		Uptime:      formatUptime(time.Since(a.startedAt)),
		StartedMs:   a.startedAt.UnixMilli(),
		BucketCount: len(buckets),
		ObjectCount: totalObjects,
		TotalBytes:  humanBytes(totalBytes),
		HitRate:     hitRate,
		Buckets:     vbuckets,
	}
}

// bucketDetail returns the object rows for one bucket, or an error
// (os.ErrNotExist / errInvalidName) the caller renders as a 404. objHost
// is the host:port of the S3 server (from the request), used to build
// each object's view-link.
func (a *AdminServer) bucketDetail(name, objHost string) (bucketView, error) {
	objs, err := a.storage.ListObjects(name)
	if err != nil {
		return bucketView{}, err
	}
	rows := make([]adminObject, 0, len(objs))
	for _, o := range objs {
		u := url.URL{Scheme: "http", Host: objHost, Path: "/" + name + "/" + o.Key}
		rows = append(rows, adminObject{
			Key:         o.Key,
			URL:         u.String(),
			Size:        humanBytes(o.Size),
			ContentType: o.ContentType,
			ACL:         o.ACL,
			Created:     formatCreated(o.CreatedAt),
		})
	}
	return bucketView{Name: name, Objects: rows}, nil
}

// s3Host derives the S3 server's host:port from the admin request's Host
// header (same hostname the user is browsing) and the configured S3 port.
func (a *AdminServer) s3Host(r *http.Request) string {
	hostname := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		hostname = h
	}
	return net.JoinHostPort(hostname, a.s3Port)
}

// --- handlers ---

func (a *AdminServer) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *AdminServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	a.render(w, "dashboard", a.dashboard())
}

func (a *AdminServer) handleOverview(w http.ResponseWriter, r *http.Request) {
	// Only the server-derived stat blocks — uptime is a client-side clock.
	a.render(w, "overview_stats", a.dashboard())
}

func (a *AdminServer) handleFragment(w http.ResponseWriter, r *http.Request) {
	a.render(w, "buckets_section", a.dashboard())
}

func (a *AdminServer) handleBucket(w http.ResponseWriter, r *http.Request) {
	v, err := a.bucketDetail(r.PathValue("name"), a.s3Host(r))
	if err != nil {
		a.notFound(w)
		return
	}
	a.render(w, "bucket", v)
}

func (a *AdminServer) handleBucketFragment(w http.ResponseWriter, r *http.Request) {
	v, err := a.bucketDetail(r.PathValue("name"), a.s3Host(r))
	if err != nil {
		a.notFound(w)
		return
	}
	a.render(w, "bucket_content", v)
}

// notFound serves an HTML 404 (this is the admin UI, not the S3 API, so
// it does not use the S3 XML error shape).
func (a *AdminServer) notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Not found — essie3 admin</title><main><h1>404</h1><p>No such bucket. <a href="/">Back to dashboard</a></p></main>`)
}

func (a *AdminServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	backlog, ch, cancel := a.broker.Subscribe()
	defer cancel()

	for _, e := range backlog {
		writeSSE(w, e)
	}
	flusher.Flush()

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, e)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (a *AdminServer) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(adminCSS)
}

func (a *AdminServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}

// --- helpers ---

// sseEvent is the JSON shape the browser feed consumes.
type sseEvent struct {
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	Method  string    `json:"method"`
	Bucket  string    `json:"bucket"`
	Key     string    `json:"key"`
	Status  int       `json:"status"`
	Bytes   int       `json:"bytes"`
	Outcome string    `json:"outcome"`
}

func writeSSE(w io.Writer, e TrafficEvent) {
	data, err := json.Marshal(sseEvent{
		Seq:     e.Seq,
		Time:    e.Time,
		Method:  e.Method,
		Bucket:  e.Bucket,
		Key:     e.Key,
		Status:  e.Status,
		Bytes:   e.Bytes,
		Outcome: e.Outcome,
	})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, data)
}

func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

func formatCreated(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
