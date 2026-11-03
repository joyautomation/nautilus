package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// movingCluster is a cluster whose status changes while a request waits.
type movingCluster struct {
	mu sync.Mutex
	st leader.Status
}

func (m *movingCluster) Status() leader.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *movingCluster) set(st leader.Status) {
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
}

// deadAddr is an address that refuses connections: a leader that has
// closed its HTTP port on the way down.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// #483: a leader shutting down closes its HTTP port before it hands the
// lease over, so for a few seconds a standby forwards to an address that
// refuses. The request never reached the old leader, so the standby waits
// for the lease to move and replays it, body and all, to the new leader.
func TestStandbyReplaysARefusedForwardToTheNewLeader(t *testing.T) {
	defer func(d time.Duration) { failoverWait = d }(failoverWait)
	failoverWait = 5 * time.Second
	var got string
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer next.Close()
	c := &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: deadAddr(t)}}
	srv := New(newTestRuntime(t), Options{Cluster: c})
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.set(leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-2", LeaderAddr: standbyAddr(next)})
	}()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", strings.NewReader(`{"name":"SP","value":5}`)))
	if rec.Code != http.StatusNoContent || got != `POST /api/tags {"name":"SP","value":5}` {
		t.Fatalf("status %d, new leader saw %q; want 204 and the original request", rec.Code, got)
	}
}

// When this replica is the one that takes over, it answers the request
// itself.
func TestStandbyThatTakesOverAnswersLocally(t *testing.T) {
	defer func(d time.Duration) { failoverWait = d }(failoverWait)
	failoverWait = 5 * time.Second
	c := &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: deadAddr(t)}}
	srv := New(newTestRuntime(t), Options{Cluster: c})
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.set(leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-1", IsLeader: true})
	}()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tags"`) {
		t.Fatalf("status %d body %.200s; want this replica's own /api/state", rec.Code, rec.Body)
	}
}

// With no change of leader in time, a refused forward is still a 502.
func TestStandbyRefusedForwardWithNoNewLeaderIs502(t *testing.T) {
	defer func(d time.Duration) { failoverWait = d }(failoverWait)
	failoverWait = 300 * time.Millisecond
	c := &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: deadAddr(t)}}
	srv := New(newTestRuntime(t), Options{Cluster: c})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
}
