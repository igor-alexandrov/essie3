package main

import (
	"net/http"
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
