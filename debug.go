package main

import (
	"net/http"
	"sort"
	"strings"
)

// debugResponseWriter wraps an http.ResponseWriter so the debug
// middleware can record the final status code and total bytes written
// without changing what the client sees.
type debugResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newDebugResponseWriter(w http.ResponseWriter) *debugResponseWriter {
	return &debugResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (d *debugResponseWriter) WriteHeader(code int) {
	if d.wroteHeader {
		return
	}
	d.wroteHeader = true
	d.status = code
	d.ResponseWriter.WriteHeader(code)
}

func (d *debugResponseWriter) Write(p []byte) (int, error) {
	if !d.wroteHeader {
		// Match net/http's implicit-200 behavior so a Write without a
		// prior WriteHeader still has a meaningful captured status.
		d.wroteHeader = true
	}
	n, err := d.ResponseWriter.Write(p)
	d.bytes += n
	return n, err
}

// formatRequest renders the multi-line request block: the `--> METHOD
// PATH` line followed by one indented line per header value, sorted by
// header name for stable, diff-friendly output.
func formatRequest(r *http.Request) string {
	var b strings.Builder
	b.WriteString("--> ")
	b.WriteString(r.Method)
	b.WriteByte(' ')
	b.WriteString(r.URL.RequestURI())
	b.WriteByte('\n')
	writeHeaders(&b, r.Header)
	return b.String()
}

// writeHeaders writes header values to b in alphabetical order by name,
// one indented line per value (multi-value headers get one line each).
func writeHeaders(b *strings.Builder, h http.Header) {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range h[name] {
			b.WriteString("    ")
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteByte('\n')
		}
	}
}
