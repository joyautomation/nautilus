package alarm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLogNotifier(t *testing.T) {
	n := NewLogNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := n.Notify(context.Background(), ev(1000, "A", "active")); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookPostsTheEventAsJSON(t *testing.T) {
	var got Event
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if k := r.Header.Get("X-Key"); k != "s3cret" {
			t.Errorf("custom header = %q", k)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	n := NewWebhook(srv.URL, WebhookOptions{Headers: map[string]string{"X-Key": "s3cret"}})
	if err := n.Notify(context.Background(), ev(1000, "A.HH", "active")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.ID != "A.HH" || got.Kind != "active" || got.Priority != High {
		t.Fatalf("webhook received %+v", got)
	}
}

func TestWebhookRetriesThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := NewWebhook(srv.URL, WebhookOptions{Retries: 2, Backoff: time.Millisecond})
	if err := n.Notify(context.Background(), ev(1000, "A", "active")); err != nil {
		t.Fatalf("Notify should have succeeded on the third attempt: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestWebhookGivesUpAndReportsTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	n := NewWebhook(srv.URL, WebhookOptions{Retries: 1, Backoff: time.Millisecond})
	err := n.Notify(context.Background(), ev(1000, "A", "active"))
	if err == nil {
		t.Fatal("want an error after the retries are spent")
	}
}

// countingNotifier records what the engine handed it.
type countingNotifier struct {
	mu   sync.Mutex
	seen []Event
}

func (c *countingNotifier) Notify(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, e)
	return nil
}

func (c *countingNotifier) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// TestEngineNotifiesOffTheScanGoroutine — the engine must hand events over
// and keep scanning, never await a notifier.
func TestEngineNotifiesOffTheScanGoroutine(t *testing.T) {
	clk, tg, n := newClock(), tags{}, &countingNotifier{}
	e, err := New(Options{
		Defs: []Def{def("A", "A")}, Read: tg.read, Now: clk.now,
		Journal: NewRing(10), Notify: []Notifier{n},
	})
	if err != nil {
		t.Fatal(err)
	}
	tg["A"] = true
	e.Evaluate()
	tg["A"] = false
	clk.advance(time.Second)
	e.Evaluate()

	// Close drains the queue, so this is deterministic without a sleep.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if got := n.len(); got != 2 {
		t.Fatalf("notifier saw %d events, want 2", got)
	}
	if n.seen[0].Kind != KindActive || n.seen[1].Kind != KindRTN {
		t.Fatalf("notifier saw %+v", n.seen)
	}
	if e.Dropped() != 0 {
		t.Fatalf("dropped %d events with an idle notifier", e.Dropped())
	}
}
