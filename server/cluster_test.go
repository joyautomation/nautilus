package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))

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
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
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
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
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

// A replica the lease still names but whose gate is closed (a hung scan's
// step-down, #621) advertises its own address as the leader's: it must not
// forward to it, or one request becomes an endless chain of self-hops. It
// answers reads from its own store and refuses writes with 503.
func TestGateClosedLeaseHolderDoesNotProxyToItself(t *testing.T) {
	var hits atomic.Int64
	self := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer self.Close()
	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-0", Leader: "plc-0", LeaderAddr: standbyAddr(self),
		IsLeader: false,
	}}})
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/state = %d, want 200 from the local store: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/forces", strings.NewReader(`{"name":"Level","value":1}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/forces = %d, want 503: %s", rec.Code, rec.Body)
	}
	if n := srv.rt.Tags().ForceCount(); n != 0 {
		t.Fatalf("a replica with its gate closed forced its own store (%d)", n)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the request was forwarded to the replica's own address %d times", n)
	}
}

// A standing warning of the redundancy (a pair's retained values not
// replicated yet, #710) reaches /api/cluster, on the leader and on a
// standby alike (/api/cluster is never proxied), and the dashboard shows
// what it carries.
func TestClusterEndpointCarriesWarnings(t *testing.T) {
	const warn = "retained state not replicated: test"
	for _, lead := range []bool{true, false} {
		srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
			Mode: "quorum", Pod: "a", Leader: "b", LeaderAddr: "127.0.0.1:1", IsLeader: lead, Warnings: []string{warn},
		}}})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
		var st leader.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if len(st.Warnings) != 1 || st.Warnings[0] != warn {
			t.Errorf("leader %v: warnings %q, want %q", lead, st.Warnings, warn)
		}
	}
	page := string(indexHTML)
	for _, want := range []string{`id="cluster-warn"`, `fetch("/api/cluster")`, `st.warnings`} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard does not show the cluster's warnings: no %s", want)
		}
	}
}

// A redundant pair's standby that knows no leader refuses a write (503 no
// leader) rather than taking it into a store that does not scan (#1028
// review), and still answers reads; the Kubernetes mode is unchanged.
func TestPairStandbyWithNoLeaderRefusesWrites(t *testing.T) {
	for _, c := range []struct {
		mode string
		want int
	}{{"quorum", http.StatusServiceUnavailable}, {"cluster", http.StatusNoContent}} {
		srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{Mode: c.mode, Pod: "a"}}})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader(`{"name":"x","value":1}`)))
		if c.mode == "quorum" && (rec.Code != c.want || !strings.Contains(rec.Body.String(), "no leader")) {
			t.Errorf("%s: a write with no leader known = %d %s, want %d no leader", c.mode, rec.Code, rec.Body, c.want)
		}
		if c.mode == "cluster" && rec.Code == http.StatusServiceUnavailable {
			t.Errorf("cluster mode refused a write with no leader known: %s", rec.Body)
		}
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/meta", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: a read with no leader known = %d", c.mode, rec.Code)
		}
	}
}

// TestClusterEndpointNamesThePeersFromPublicURLs: a browser whose own
// controller died needs the other one's address (#716). Only the URLs the
// operator named (redundancy.public-urls) go out, sorted, with this
// replica's view of who leads; LeaderAddr still never does.
func TestClusterEndpointNamesThePeersFromPublicURLs(t *testing.T) {
	srv := New(newTestRuntime(t), Options{
		Cluster:    fakeCluster{status: leader.Status{Mode: "cluster", Pod: "a", Leader: "b", LeaderAddr: "10.0.0.7:8080"}},
		LeaderURLs: map[string]string{"b": "https://plc-b.plant.example", "a": "https://plc-a.plant.example"},
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Fatalf("a peer-link address leaked: %s", rec.Body.String())
	}
	var body struct {
		Leader string        `json:"leader"`
		Peers  []clusterPeer `json:"peers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []clusterPeer{{"a", "https://plc-a.plant.example", false}, {"b", "https://plc-b.plant.example", true}}
	if body.Leader != "b" || len(body.Peers) != 2 || body.Peers[0] != want[0] || body.Peers[1] != want[1] {
		t.Errorf("body = %+v", body)
	}

	// No public-urls: no peers key at all.
	srv = New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{Mode: "cluster", Pod: "a"}}})
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/cluster", nil))
	if strings.Contains(rec.Body.String(), "peers") {
		t.Errorf("peers without public-urls: %s", rec.Body.String())
	}
}
