package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// TrafficEvent is one observed request, published to the broker by the
// capture middleware and rendered in the live feed.
type TrafficEvent struct {
	Seq     uint64 // monotonic, assigned by the broker; used as the SSE id
	Time    time.Time
	Method  string
	Bucket  string
	Key     string // "" for bucket-level requests
	Status  int
	Bytes   int
	Outcome string // see classifyOutcome
}

// subBuffer is the per-subscriber channel buffer. A subscriber that
// falls this far behind drops events rather than stalling the request
// path (see Publish).
const subBuffer = 128

// TrafficBroker keeps the last N events in a ring buffer and fans new
// events out to live subscribers. Safe for concurrent publish and
// subscribe.
type TrafficBroker struct {
	mu   sync.Mutex
	ring []TrafficEvent // len <= cap, oldest-first
	cap  int
	seq  uint64
	subs map[chan TrafficEvent]struct{}
	// counters for the stats page
	reads     uint64 // GET/HEAD reads served: real object, fallback, or miss
	fallbacks uint64 // subset of reads served by a fallback
}

// NewTrafficBroker returns a broker retaining up to capacity events.
func NewTrafficBroker(capacity int) *TrafficBroker {
	if capacity < 1 {
		capacity = 1
	}
	return &TrafficBroker{
		cap:  capacity,
		ring: make([]TrafficEvent, 0, capacity),
		subs: make(map[chan TrafficEvent]struct{}),
	}
}

// Publish assigns a Seq, appends to the ring (evicting the oldest),
// bumps the hit-rate counters, and non-blockingly sends to every
// subscriber. A subscriber whose buffered channel is full drops the
// event rather than blocking the caller (the S3 request path).
func (b *TrafficBroker) Publish(e TrafficEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seq++
	e.Seq = b.seq

	if len(b.ring) == b.cap {
		copy(b.ring, b.ring[1:])
		b.ring[b.cap-1] = e
	} else {
		b.ring = append(b.ring, e)
	}

	// Hit rate = fallbacks / all reads, so the denominator counts every
	// GET/HEAD that was served or missed (real, fallback, or miss) — not
	// just misses, which would peg the rate at 0% or 100%.
	switch e.Outcome {
	case "real", "miss":
		b.reads++
	case "fallback":
		b.reads++
		b.fallbacks++
	}

	for ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber is behind; drop for it only
		}
	}
}

// Subscribe returns a snapshot of the ring (backlog replay), a channel
// of future events, and a cancel func the caller defers to unsubscribe.
func (b *TrafficBroker) Subscribe() (backlog []TrafficEvent, ch chan TrafficEvent, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	backlog = make([]TrafficEvent, len(b.ring))
	copy(backlog, b.ring)

	ch = make(chan TrafficEvent, subBuffer)
	b.subs[ch] = struct{}{}

	var once sync.Once
	cancel = func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subs, ch)
			close(ch)
		})
	}
	return backlog, ch, cancel
}

// Stats returns the hit-rate counters the dashboard needs.
func (b *TrafficBroker) Stats() (reads, fallbacks uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads, b.fallbacks
}

// WithTrafficCapture wraps next so every request is published to the
// broker after it completes. Independent of WithDebugLogging; both may
// wrap the same handler in either order.
func WithTrafficCapture(next http.Handler, broker *TrafficBroker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		crw := newCountingResponseWriter(w)
		next.ServeHTTP(crw, r)
		bucket, key := splitBucketKey(r.URL.Path)
		broker.Publish(TrafficEvent{
			Time:    start,
			Method:  r.Method,
			Bucket:  bucket,
			Key:     key,
			Status:  crw.status,
			Bytes:   crw.bytes,
			Outcome: classifyOutcome(r.Method, crw.status, crw.Header().Get("X-Essie3-Fallback")),
		})
	})
}

// splitBucketKey mirrors Handler.ServeHTTP's own /bucket/key split so
// the feed labels match how the handler routed the request.
func splitBucketKey(path string) (bucket, key string) {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	bucket = parts[0]
	if len(parts) == 2 {
		key = parts[1]
	}
	return bucket, key
}

// classifyOutcome maps (method, status, fallback-header) to a short
// label for the feed. A non-empty fallback header always wins, since it
// is only ever set on a served fallback response.
func classifyOutcome(method string, status int, fallback string) string {
	if fallback != "" {
		return "fallback"
	}
	if status == http.StatusForbidden {
		return "denied"
	}
	switch method {
	case http.MethodGet, http.MethodHead:
		if status == http.StatusNotFound {
			return "miss"
		}
		if status < 400 {
			return "real"
		}
	case http.MethodPut, http.MethodPost:
		if status < 400 {
			return "write"
		}
	case http.MethodDelete:
		if status < 400 {
			return "delete"
		}
	}
	return "other"
}
