package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
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
	tmpl      *template.Template
}

// NewAdminServer parses the embedded template once and returns a ready
// server. The template's root is the full page; its "content" block is
// the soft-refreshable stats+buckets region reused by /fragment.
func NewAdminServer(s *Storage, fb *Fallback, b *TrafficBroker, startedAt time.Time) *AdminServer {
	return &AdminServer{
		storage:   s,
		fallback:  fb,
		broker:    b,
		startedAt: startedAt,
		tmpl:      template.Must(template.New("admin").Parse(adminTemplate)),
	}
}

// Handler wires the read-only routes. Method+path patterns (Go 1.22)
// give unknown paths a 404 and wrong methods a 405 for free.
func (a *AdminServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /fragment", a.handleFragment)
	mux.HandleFunc("GET /events", a.handleEvents)
	mux.HandleFunc("GET /assets/pico.classless.min.css", a.handleCSS)
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	return mux
}

// --- view model ---

type adminView struct {
	Uptime      string
	BucketCount int
	ObjectCount int
	TotalBytes  string
	HitRate     string
	Buckets     []adminBucket
}

type adminBucket struct {
	Name        string
	ObjectCount int
	TotalBytes  string
	Objects     []adminObject
}

type adminObject struct {
	Key         string
	Size        string
	ContentType string
	ACL         string
	Created     string
}

func (a *AdminServer) view() adminView {
	reads, fallbacks := a.broker.Stats()
	hitRate := "n/a"
	if reads > 0 {
		hitRate = fmt.Sprintf("%.0f%%", float64(fallbacks)/float64(reads)*100)
	}

	buckets, _ := a.storage.ListBuckets()
	vbuckets := make([]adminBucket, 0, len(buckets))
	var totalObjects int
	var totalBytes int64
	for _, b := range buckets {
		objs, _ := a.storage.ListObjects(b.Name)
		rows := make([]adminObject, 0, len(objs))
		for _, o := range objs {
			rows = append(rows, adminObject{
				Key:         o.Key,
				Size:        humanBytes(o.Size),
				ContentType: o.ContentType,
				ACL:         o.ACL,
				Created:     formatCreated(o.CreatedAt),
			})
		}
		vbuckets = append(vbuckets, adminBucket{
			Name:        b.Name,
			ObjectCount: b.ObjectCount,
			TotalBytes:  humanBytes(b.TotalBytes),
			Objects:     rows,
		})
		totalObjects += b.ObjectCount
		totalBytes += b.TotalBytes
	}

	return adminView{
		Uptime:      formatUptime(time.Since(a.startedAt)),
		BucketCount: len(buckets),
		ObjectCount: totalObjects,
		TotalBytes:  humanBytes(totalBytes),
		HitRate:     hitRate,
		Buckets:     vbuckets,
	}
}

// --- handlers ---

func (a *AdminServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.Execute(w, a.view()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *AdminServer) handleFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.ExecuteTemplate(w, "content", a.view()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
