package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/leader"
)

// fakeCluster is a minimal stand-in for *leader.Elector — anything with a
// Status() leader.Status method satisfies Options.Cluster, so a test doesn't
// need a real Lease or Kubernetes API to exercise the server's redundancy
// wiring.
type fakeCluster struct {
	status leader.Status
}

func (f fakeCluster) Status() leader.Status { return f.status }

// TestClusterEndpointReportsStatus checks that GET /api/cluster serves
// exactly what the configured Cluster returns, and that LeaderAddr — which
// carries a pod's raw address — never reaches the response body thanks to
// its json:"-" tag; a browser has no business seeing pod IPs.
func TestClusterEndpointReportsStatus(t *testing.T) {
	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-0", Leader: "plc-0", LeaderAddr: "10.0.0.5:8080",
		IsLeader: true, Transitions: 2,
	}}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/cluster", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("LeaderAddr leaked into the response: %s", rec.Body.String())
	}
	var st leader.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != "cluster" || st.Pod != "plc-0" || st.Leader != "plc-0" || !st.IsLeader || st.Transitions != 2 {
		t.Errorf("status = %+v", st)
	}
}

// TestClusterEndpointNilCluster asserts a server with no Cluster configured
// still answers /api/cluster (rather than 404ing) as a standalone leader —
// so a dashboard never needs a special case for "redundancy isn't wired up".
func TestClusterEndpointNilCluster(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/cluster", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var st leader.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != "standalone" || !st.IsLeader {
		t.Errorf("status = %+v, want standalone leader", st)
	}
}

// standbyAddr strips the http:// scheme off an httptest.Server URL, since
// leader.Status.LeaderAddr is a bare host:port (what the reverse proxy's
// url.URL.Host wants), never a full URL.
func standbyAddr(ts *httptest.Server) string {
	return strings.TrimPrefix(ts.URL, "http://")
}

// TestStandbyProxiesToLeader is the core redundancy behavior: a standby
// (IsLeader false, with a known LeaderAddr) must not answer /api/state from
// its own stale, non-scanning tag store — it forwards the request to
// whoever holds the lease and relays the response back untouched.
func TestStandbyProxiesToLeader(t *testing.T) {
	hit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Path != "/api/state" {
			t.Errorf("backend saw path %q, want /api/state", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"marker":"from-leader"}`))
	}))
	defer backend.Close()

	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: standbyAddr(backend),
		IsLeader: false,
	}}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))

	if !hit {
		t.Fatal("standby answered /api/state locally instead of proxying to the leader")
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "from-leader") {
		t.Errorf("body = %s, want the backend's response to pass through", rec.Body)
	}
}

// TestStandbyAnswersClusterAndIndexLocally checks the two exceptions a
// standby always serves itself: /api/cluster (each replica reports its own
// view — that's the point) and the static UI, so a probe or browser always
// gets a page from whichever replica it lands on.
func TestStandbyAnswersClusterAndIndexLocally(t *testing.T) {
	hit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer backend.Close()

	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: standbyAddr(backend),
		IsLeader: false,
	}}})
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/cluster", nil))
	if rec.Code != 200 {
		t.Fatalf("/api/cluster status = %d", rec.Code)
	}
	var st leader.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Pod != "plc-1" || st.IsLeader {
		t.Errorf("/api/cluster should report this replica's own view, got %+v", st)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want the local dashboard", ct)
	}

	if hit {
		t.Error("a standby proxied /api/cluster or / to the leader; both must be answered locally")
	}
}

// TestStandbyWithNoLeaderServesLocally checks the fall-through case: when
// LeaderAddr is empty (no leader elected yet, or a standalone-mode elector
// that never sets one), a standby handles the request itself rather than
// failing it — there's nowhere better to send it.
func TestStandbyWithNoLeaderServesLocally(t *testing.T) {
	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "", LeaderAddr: "",
		IsLeader: false,
	}}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("expected a local Frame, got: %s", rec.Body)
	}
}

// TestLeaderNeverProxies checks that IsLeader true always short-circuits the
// proxy path — even when LeaderAddr happens to be set (e.g. it points at
// this replica's own address).
func TestLeaderNeverProxies(t *testing.T) {
	hit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer backend.Close()

	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-0", Leader: "plc-0", LeaderAddr: standbyAddr(backend),
		IsLeader: true,
	}}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("expected a local Frame, got: %s", rec.Body)
	}
	if hit {
		t.Error("the leader proxied a request instead of answering it locally")
	}
}
