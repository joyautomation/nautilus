package prom

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// An insecure: source reuses its connection: one per poll, never closed,
// was a leak of a socket and its goroutines every scrape.
func TestInsecureFetchReusesConnections(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("up 1\n"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	defer srv.Close()
	for i := 0; i < 5; i++ {
		if _, err := httpFetch(context.Background(), srv.URL, nil, true, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("5 scrapes opened %d connections, want 1", n)
	}
}
