package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrafficBroker_RingEviction(t *testing.T) {
	b := NewTrafficBroker(3)
	for i := 0; i < 5; i++ {
		b.Publish(TrafficEvent{Method: "GET"})
	}
	backlog, _, cancel := b.Subscribe()
	defer cancel()

	if len(backlog) != 3 {
		t.Fatalf("backlog len = %d, want 3", len(backlog))
	}
	// Seq is monotonic and the backlog holds the last 3 (seq 3,4,5).
	wantSeqs := []uint64{3, 4, 5}
	for i, e := range backlog {
		if e.Seq != wantSeqs[i] {
			t.Errorf("backlog[%d].Seq = %d, want %d", i, e.Seq, wantSeqs[i])
		}
	}
}

func TestTrafficBroker_LiveFanoutAndCancel(t *testing.T) {
	b := NewTrafficBroker(10)
	_, ch, cancel := b.Subscribe()

	b.Publish(TrafficEvent{Method: "GET", Outcome: "real"})
	select {
	case e := <-ch:
		if e.Method != "GET" || e.Outcome != "real" {
			t.Fatalf("got %+v", e)
		}
	default:
		t.Fatal("expected a live event on the channel")
	}

	cancel()
	// After cancel, a later publish must not block and must not deliver.
	b.Publish(TrafficEvent{Method: "PUT"})
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestTrafficBroker_SlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewTrafficBroker(10)
	_, _, cancel := b.Subscribe() // never drained
	defer cancel()

	// Publish far more than any channel buffer; must return, never block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			b.Publish(TrafficEvent{Method: "GET"})
		}
		close(done)
	}()
	<-done
}

func TestTrafficBroker_Counters(t *testing.T) {
	b := NewTrafficBroker(100)
	// 2 fallbacks, 1 miss, plus some non-read events that must not count.
	b.Publish(TrafficEvent{Outcome: "fallback"})
	b.Publish(TrafficEvent{Outcome: "fallback"})
	b.Publish(TrafficEvent{Outcome: "miss"})
	b.Publish(TrafficEvent{Outcome: "real"})
	b.Publish(TrafficEvent{Outcome: "write"})

	reads, fallbacks := b.Stats()
	if reads != 3 {
		t.Errorf("reads = %d, want 3", reads)
	}
	if fallbacks != 2 {
		t.Errorf("fallbacks = %d, want 2", fallbacks)
	}
}

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		status   int
		fallback string
		want     string
	}{
		{"fallback pool", "GET", 200, "pool", "fallback"},
		{"fallback generate", "GET", 206, "generate", "fallback"},
		{"denied", "GET", 403, "", "denied"},
		{"get miss", "GET", 404, "", "miss"},
		{"head miss", "HEAD", 404, "", "miss"},
		{"get real 200", "GET", 200, "", "real"},
		{"get real 206", "GET", 206, "", "real"},
		{"get real 304", "GET", 304, "", "real"},
		{"put write", "PUT", 200, "", "write"},
		{"post write", "POST", 204, "", "write"},
		{"delete", "DELETE", 204, "", "delete"},
		{"get error other", "GET", 400, "", "other"},
		{"put error other", "PUT", 400, "", "other"},
		{"options other", "OPTIONS", 200, "", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOutcome(tt.method, tt.status, tt.fallback); got != tt.want {
				t.Errorf("classifyOutcome(%q,%d,%q) = %q, want %q",
					tt.method, tt.status, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestSplitBucketKey(t *testing.T) {
	tests := []struct {
		path       string
		wantBucket string
		wantKey    string
	}{
		{"/b", "b", ""},
		{"/b/k", "b", "k"},
		{"/b/a/c", "b", "a/c"},
		{"/", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		bucket, key := splitBucketKey(tt.path)
		if bucket != tt.wantBucket || key != tt.wantKey {
			t.Errorf("splitBucketKey(%q) = (%q,%q), want (%q,%q)",
				tt.path, bucket, key, tt.wantBucket, tt.wantKey)
		}
	}
}

func TestWithTrafficCapture(t *testing.T) {
	b := NewTrafficBroker(10)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Essie3-Fallback", "pool")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})
	srv := httptest.NewServer(WithTrafficCapture(next, b))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	backlog, _, cancel := b.Subscribe()
	defer cancel()
	if len(backlog) != 1 {
		t.Fatalf("backlog len = %d, want 1", len(backlog))
	}
	e := backlog[0]
	if e.Method != "GET" || e.Bucket != "assets" || e.Key != "logo.png" {
		t.Errorf("event routing wrong: %+v", e)
	}
	if e.Status != 200 || e.Bytes != 5 {
		t.Errorf("event status/bytes wrong: %+v", e)
	}
	if e.Outcome != "fallback" {
		t.Errorf("outcome = %q, want fallback", e.Outcome)
	}
}
