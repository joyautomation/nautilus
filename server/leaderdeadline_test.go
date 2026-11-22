package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// hungLeader is a stand-in leader that takes a request and never answers
// it, until the test ends. With a certificate it serves TLS, the way naut
// run does.
func hungLeader(t *testing.T, cp, kp string) (addr string, hits func() int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	release := make(chan struct{})
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	served := ln
	if cp != "" {
		src, err := NewCertSource(cp, kp)
		if err != nil {
			t.Fatal(err)
		}
		served = tls.NewListener(ln, src.Config())
	}
	go hs.Serve(served)
	t.Cleanup(func() { close(release); hs.Close() })
	return ln.Addr().String(), n.Load
}

// A standby forwarding to a leader that took the request and never answers
// gives the client a 502 once the header deadline passes (#710), over plain
// HTTP and over server.tls, instead of holding it for as long as the client
// waits. The request reached the leader, so it is not replayed.
func TestStandbyForwardToAHungLeaderIs502(t *testing.T) {
	defer func(d time.Duration) { leaderHeaderTimeout = d }(leaderHeaderTimeout)
	leaderHeaderTimeout = 300 * time.Millisecond
	for _, tc := range []struct {
		name string
		tls  bool
	}{{"plain", false}, {"tls", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var o Options
			var cp, kp string
			if tc.tls {
				cp, kp, _ = writeCert(t, t.TempDir(), "plc", "plc.example")
				mine, err := NewCertSource(cp, kp)
				if err != nil {
					t.Fatal(err)
				}
				o.TLS = mine
			}
			addr, hits := hungLeader(t, cp, kp)
			o.Cluster = &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: addr}}
			srv := auditServer(t, o)
			done := make(chan *httptest.ResponseRecorder, 1)
			start := time.Now()
			go func() {
				req := httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader(`{"name":"SP","value":70}`))
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				done <- rec
			}()
			select {
			case rec := <-done:
				if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "awaiting response headers") {
					t.Fatalf("a hung leader: %d %s, want a 502 for the header deadline", rec.Code, rec.Body)
				}
				if took := time.Since(start); took > 5*time.Second {
					t.Fatalf("the 502 took %v", took)
				}
				if hits() != 1 {
					t.Fatalf("the leader saw %d requests, want 1 (not replayed)", hits())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a standby forwarding to a leader that never answers held the request past 5 s: no header deadline")
			}
		})
	}
}

// The deadlines are on both transports a standby forwards with.
func TestLeaderTransportsHaveDeadlines(t *testing.T) {
	cp, kp, _ := writeCert(t, t.TempDir(), "plc", "plc.example")
	mine, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(newTestRuntime(t), Options{TLS: mine})
	for name, rt := range map[string]leaderTransport{"plain": srv.leaderPlain, "tls": srv.leaderTLS} {
		tr, ok := rt.(*http.Transport)
		if !ok {
			t.Fatalf("%s: %T", name, rt)
		}
		if tr.TLSHandshakeTimeout != leaderHandshakeTimeout || tr.ResponseHeaderTimeout != leaderHeaderTimeout || tr.DialContext == nil {
			t.Errorf("%s: handshake %v, headers %v, dialer set %v", name, tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout, tr.DialContext != nil)
		}
	}
	if NewPlayground(newTestRuntime(t)).leaderPlain != nil {
		t.Error("the playground has a leader transport")
	}
}

// A dial that runs out of time is a dial error: the leader never saw the
// request, so the standby waits for the lease to move and sends it on.
// Here this replica takes over and answers it itself.
func TestStandbyReplaysAForwardWhoseDialTimedOut(t *testing.T) {
	defer func(d, f time.Duration) { leaderDialTimeout, failoverWait = d, f }(leaderDialTimeout, failoverWait)
	leaderDialTimeout, failoverWait = time.Nanosecond, 5*time.Second
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var reached atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			conn.Close()
		}
	}()
	c := &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: ln.Addr().String()}}
	srv := New(newTestRuntime(t), Options{Cluster: c})
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.set(leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-1", IsLeader: true})
	}()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tags"`) {
		t.Fatalf("status %d body %.200s; want the dial timeout replayed to this replica, now the leader", rec.Code, rec.Body)
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the timed-out dial reached the old leader %d times", n)
	}
}
