package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/runtime"
)

// pairRig is a commissioned pair on loopback, wired as naut run wires it
// (startPairNodeAPI): a at 127.0.0.1, b at 127.0.0.2, both tag APIs on one
// port, and naut witness.
type pairRig struct {
	t       *testing.T
	files   fstest.MapFS
	env     map[string]map[string]string
	peer    map[string]string // peer listener address
	host    map[string]string
	apiPort string
	nodes   map[string]*pairNode
	pmod    func(n string, p *project.Project) // nil: the project as loaded
	dmod    func(n string, d *pairDeps)        // nil: the member's deps as built
	// The witness: its address, state directory and token, and its stop
	// (stopWitness, startWitness).
	wit, witDir, token string
	wstop              func()
	// witArgs are more flags for naut witness (--peer-tls=off); plain:
	// the rig's own calls to a peer port go as plain HTTP.
	witArgs []string
	plain   bool
}

func newPairRig(t *testing.T) *pairRig {
	t.Helper()
	return newPairRigWith(t, nil)
}

// newPairRigWith is newPairRig with pmod applied to each member's project
// (at every start, restarts included).
func newPairRigWith(t *testing.T, pmod func(n string, p *project.Project)) *pairRig {
	t.Helper()
	return newPairRigFull(t, pmod, nil)
}

// newPairRigFull is newPairRigWith with dmod applied to each member's
// pairDeps too (its credentials, #708).
func newPairRigFull(t *testing.T, pmod func(n string, p *project.Project), dmod func(n string, d *pairDeps)) *pairRig {
	t.Helper()
	return newPairRigPlain(t, pmod, dmod, false)
}

// newPairRigPlain is newPairRigFull, with peer TLS off on all three when
// plain (redundancy.peer-tls: off, naut witness --peer-tls=off).
func newPairRigPlain(t *testing.T, pmod func(n string, p *project.Project), dmod func(n string, d *pairDeps), plain bool) *pairRig {
	t.Helper()
	if plain {
		inner := pmod
		pmod = func(n string, p *project.Project) {
			p.Redundancy.PeerTLS = "off"
			if inner != nil {
				inner(n, p)
			}
		}
	}
	if testing.Short() {
		t.Skip("runs a pair and a witness on loopback for several leases")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := &pairRig{t: t, host: map[string]string{"a": "127.0.0.1", "b": "127.0.0.2"}, peer: map[string]string{},
		env: map[string]map[string]string{}, nodes: map[string]*pairNode{}, pmod: pmod, dmod: dmod, plain: plain}
	if plain {
		r.witArgs = []string{"--peer-tls=off"}
	}
	lnA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lnB, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		lnA.Close()
		t.Skip("no 127.0.0.2 on this host:", err)
	}
	apiA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, r.apiPort, _ = net.SplitHostPort(apiA.Addr().String())
	apiB, err := net.Listen("tcp", "127.0.0.2:"+r.apiPort)
	if err != nil {
		apiA.Close()
		t.Skip("127.0.0.2 does not take the same API port:", err)
	}
	cluster, token := node.NewClusterID(), testPeerToken
	r.token, r.witDir = token, t.TempDir()
	r.startWitness("127.0.0.1:0")
	t.Cleanup(r.stopWitness)
	wit := r.wit
	r.peer["a"], r.peer["b"] = lnA.Addr().String(), lnB.Addr().String()
	r.files = pairProject(r.peer["a"], r.peer["b"], wit)
	if testPairFiles != nil {
		r.files = testPairFiles(r.peer["a"], r.peer["b"], wit)
	}
	for _, n := range []string{"a", "b"} {
		r.env[n] = map[string]string{"NAUTILUS_NODE": n, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
		if testPairAPIToken != "" {
			r.env[n]["NAUTILUS_TOKEN"] = testPairAPIToken
		}
	}
	r.nodes["a"] = r.start("a", lnA, apiA)
	r.nodes["b"] = r.start("b", lnB, apiB)
	a, b := r.elector("a"), r.elector("b")
	pairWait(t, "both members to hear the witness", 10*time.Second, func() bool {
		return a.Status().WitnessIncarnation != 0 && b.Status().WitnessIncarnation != 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Recommission(ctx, a.Status().WitnessIncarnation, max(a.KnownFloor(), b.KnownFloor())+1000); err != nil {
		t.Fatal(err)
	}
	r.settled()
	return r
}

// settled waits for the new pair to settle: one leader, its log writable,
// for a full second (two leases) without a change. Right after
// commissioning the lead can move once (a's lease rests on b's grant
// while the witness is in its quiet period), and a new leader's view of
// the other copies lags by a ping, so a write then is refused (#1053 CI).
func (r *pairRig) settled() {
	r.t.Helper()
	var who string
	var since time.Time
	pairWait(r.t, "the pair to settle (one leader, its log writable, for a second)", 20*time.Second, func() bool {
		cur := ""
		for _, n := range []string{"a", "b"} {
			if r.nodes[n].m.q.IsLeader() && r.nodes[n].rt.Leading() && r.nodes[n].m.log.Status().Writable {
				cur = n
			}
		}
		if cur == "" || cur != who {
			who, since = cur, time.Now()
			return false
		}
		return time.Since(since) >= time.Second
	})
}

// logReady waits until member n's operator-intent log can commit a write:
// at a new pair's start, until the vouched leader's first heartbeat has
// synced the other two copies (#710 3b-4b).
func (r *pairRig) logReady(n string) {
	r.t.Helper()
	pairWait(r.t, n+"'s operator-intent log to be writable", 15*time.Second, func() bool {
		return r.nodes[n].m.log.Status().Writable
	})
}

func (r *pairRig) elector(n string) *node.Elector { return r.nodes[n].m.q.Node() }

// startWitness runs naut witness on addr with the rig's state directory.
func (r *pairRig) startWitness(addr string) {
	r.t.Helper()
	wctx, stop := context.WithCancel(context.Background())
	wready, wdone := make(chan string, 1), make(chan int, 1)
	go func() {
		wdone <- witnessMain(wctx, append([]string{"--listen", addr, "--state", r.witDir, "--lease", "500ms"}, r.witArgs...),
			pairEnv(map[string]string{"NAUTILUS_PEER_TOKEN": r.token}), io.Discard, io.Discard, wready)
	}()
	select {
	case r.wit = <-wready:
	case code := <-wdone:
		stop()
		r.t.Fatalf("naut witness exited %d", code)
	}
	r.wstop = func() { stop(); <-wdone }
}

// stopWitness stops the witness (again: a no-op).
func (r *pairRig) stopWitness() {
	if r.wstop != nil {
		r.wstop()
		r.wstop = nil
	}
}

// leader waits for a leader whose runtime has taken over (its takeover,
// the log's catch-up included, is done) and names it; other is the other
// member.
func (r *pairRig) leader() string {
	var who string
	pairWait(r.t, "a leader", 15*time.Second, func() bool {
		for _, n := range []string{"a", "b"} {
			if r.nodes[n] != nil && r.nodes[n].m != nil && r.nodes[n].m.q.IsLeader() && r.nodes[n].rt.Leading() {
				who = n
				return true
			}
		}
		return false
	})
	return who
}

func other(n string) string {
	if n == "a" {
		return "b"
	}
	return "a"
}

// restart starts member n again on its addresses, with its state
// directory: a reboot after a crash.
func (r *pairRig) restart(n string) {
	r.t.Helper()
	ln, err := net.Listen("tcp", r.peer[n])
	if err != nil {
		r.t.Fatal(err)
	}
	api, err := net.Listen("tcp", r.host[n]+":"+r.apiPort)
	if err != nil {
		r.t.Fatal(err)
	}
	r.nodes[n] = r.start(n, ln, api)
}

// start starts member n on its listeners, with the rig's project change.
func (r *pairRig) start(n string, ln, api net.Listener) *pairNode {
	var pm func(*project.Project)
	if r.pmod != nil {
		pm = func(p *project.Project) { r.pmod(n, p) }
	}
	var dm func(*pairDeps)
	if r.dmod != nil {
		dm = func(d *pairDeps) { r.dmod(n, d) }
	}
	return startPairNodeFull(r.t, r.files, r.env[n], ln, api, dm, pm)
}

// call sends a request to member n's tag API.
func (r *pairRig) call(n, method, path, body string) (int, string, http.Header) {
	r.t.Helper()
	req, err := http.NewRequest(method, "http://"+r.host[n]+":"+r.apiPort+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// substitutes is member n's runtime's active substitutes.
func (r *pairRig) substitutes(n string) []runtime.Substitution { return r.nodes[n].rt.Substitutes() }

// startedID starts a substitute of Level through member n's API and
// returns its ID.
func (r *pairRig) startedID(n string) uint64 {
	r.t.Helper()
	// A certain refusal (nothing proposed) is retried for a few seconds:
	// right after a pair starts, the lead can move and the new leader's
	// view of the other copies lag by a ping. Anything else fails at once.
	var code int
	var body string
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var h http.Header
		code, body, h = r.call(n, "POST", "/api/substitutes", `{"name":"Level","seconds":600}`)
		if code == http.StatusOK || h.Get("X-Nautilus-Outcome") != "refused" && !strings.Contains(body, "no leader") || time.Now().After(end) {
			break
		}
	}
	if code != http.StatusOK {
		r.t.Fatalf("start through %s: %d %s", n, code, body)
	}
	var resp struct {
		Started struct {
			ID uint64 `json:"id"`
		} `json:"started"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.Started.ID == 0 {
		r.t.Fatalf("start answer %s: %v", body, err)
	}
	return resp.Started.ID
}

func hasSubstitute(subs []runtime.Substitution, id uint64) bool {
	for _, s := range subs {
		if s.ID == id {
			return true
		}
	}
	return false
}

// A substitute started through either member's API is an entry of the
// pair's operator-intent log: both members apply it, and after a clean
// handover the new leader holds the same entry, and stops it through the
// log on both.
func TestPairSubstitutesGoThroughTheLog(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	id := r.startedID(stand) // through the standby: proxied to the leader
	pairWait(t, "both members to apply the start", 5*time.Second, func() bool {
		return hasSubstitute(r.substitutes(lead), id) && hasSubstitute(r.substitutes(stand), id)
	})

	r.nodes[lead].m.q.Release()
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	if !hasSubstitute(r.substitutes(stand), id) {
		t.Fatalf("the new leader lost substitute %d at a clean handover: %+v", id, r.substitutes(stand))
	}
	if code, body, _ := r.call(stand, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop on the new leader: %d %s", code, body)
	}
	pairWait(t, "both members to apply the stop", 5*time.Second, func() bool {
		return !hasSubstitute(r.substitutes(lead), id) && !hasSubstitute(r.substitutes(stand), id)
	})
}

// §8.1 case 1 (#710 3b-4b): with the standby down, the leader and the
// witness are the log's majority, so substitutes start, are acknowledged
// and stop; the standby applies them when it returns.
func TestPairLogWritesWithItsPeerDown(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	id := r.startedID(lead)
	if code, body, _ := r.call(lead, "POST", "/api/substitutes/ack", `{"ids":[`+itoa(id)+`]}`); code != http.StatusOK {
		t.Fatalf("ack with the standby down: %d %s", code, body)
	}
	r.restart(stand)
	pairWait(t, "the returning standby to apply the start", 10*time.Second, func() bool { return hasSubstitute(r.substitutes(stand), id) })
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, body)
	}
	pairWait(t, "both to apply the stop", 10*time.Second, func() bool {
		return !hasSubstitute(r.substitutes(stand), id) && !hasSubstitute(r.substitutes(lead), id)
	})
}

// §8.1 case 5: a lone survivor (the other controller and the witness both
// down) keeps what it applied and refuses every operator-intent write for
// certain (nothing was proposed): the no-majority text while it still
// holds its lease, "no leader" once that has run out. When they return,
// writes go through again.
func TestPairLogRefusesWithoutAMajority(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	id := r.startedID(lead)
	pairWait(t, "the standby to apply the start", 5*time.Second, func() bool { return hasSubstitute(r.substitutes(stand), id) })

	r.nodes[stand].crash()
	r.stopWitness()
	pairWait(t, "the log to notice both gone (its next ping)", 10*time.Second, func() bool { return !r.nodes[lead].m.log.Status().Writable })
	for _, c := range []struct{ verb, method, path, body string }{
		{"start", "POST", "/api/substitutes", `{"name":"Level","seconds":60}`},
		{"stop", "DELETE", "/api/substitutes/" + itoa(id), ""},
		{"ack", "POST", "/api/substitutes/ack", `{"ids":[` + itoa(id) + `]}`},
	} {
		// With both others down the survivor also loses its lease, so the
		// write may meet the log's no-majority refusal or, a moment later,
		// the standby's "no leader": either is a certain refusal, never
		// "outcome unknown".
		code, body, h := r.call(lead, c.method, c.path, c.body)
		logRefusal := h.Get("X-Nautilus-Outcome") == "refused" && strings.Contains(body, PairNoMajority)
		noLeader := h.Get("X-Nautilus-Outcome") == "" && strings.Contains(body, "no leader")
		if code != http.StatusServiceUnavailable || !logRefusal && !noLeader {
			t.Errorf("%s alone: %d %q %s; want 503 refused with %q, or no leader", c.verb, code, h.Get("X-Nautilus-Outcome"), body, PairNoMajority)
		}
	}
	if !hasSubstitute(r.substitutes(lead), id) {
		t.Error("the survivor dropped the substitute it had applied")
	}

	r.startWitness(r.wit)
	r.restart(stand)
	pairWait(t, "a stop to go through once they are back", 20*time.Second, func() bool {
		who := ""
		for _, n := range []string{"a", "b"} {
			if r.nodes[n].m.q.IsLeader() && r.nodes[n].rt.Leading() {
				who = n
			}
		}
		if who == "" {
			return false
		}
		code, _, _ := r.call(who, "DELETE", "/api/substitutes/"+itoa(id), "")
		return code == http.StatusOK
	})
}

// §8.1 case 7: a substitute the leader and the witness committed while the
// standby was down is applied before the first scan of the takeover after
// the leader died: the standby's catch-up finds it on the witness. No
// __Substitute_Fault (the takeover is synced), and the new leader takes a
// stop at once, without the old one.
func TestPairLogSurvivesALeaderCrash(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	id := r.startedID(lead) // on the leader and the witness only
	r.nodes[lead].crash()
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	if !hasSubstitute(r.substitutes(stand), id) {
		t.Fatalf("the takeover after the leader died lost substitute %d, which the witness held: %+v", id, r.substitutes(stand))
	}
	fault := func(n string) bool { return r.nodes[n].rt.Tags().Bool(runtime.SubstituteFaultTag) }
	if fault(stand) {
		t.Error("a takeover synced from the witness raised __Substitute_Fault")
	}
	r.logReady(stand)
	if code, body, _ := r.call(stand, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop with the old leader still down: %d %s", code, body)
	}
	r.restart(lead)
	pairWait(t, "the old leader to apply the stop", 10*time.Second, func() bool { return !hasSubstitute(r.substitutes(lead), id) })
}

// The log's routes live on the peer listener only: the tag API answers
// 404 for them, token or not (test plan §8.3), and the peer listener
// refuses them without the peer token.
func TestPairLogRoutesAreNotOnTheTagAPI(t *testing.T) {
	r := newPairRig(t)
	for _, p := range []string{"/api/oplog?after=0", "/api/quorum"} {
		if code, body, _ := r.call("a", "GET", p, ""); code != http.StatusNotFound {
			t.Errorf("tag API GET %s: %d %s, want 404", p, code, body)
		}
		resp, err := peerClient(t, testPeerToken).Get("https://" + r.peer["a"] + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("peer listener GET %s without the token: %d, want 401", p, resp.StatusCode)
		}
		req, _ := http.NewRequest("GET", "https://"+r.peer["a"]+p, nil)
		req.Header.Set(node.TokenHeader, testPeerToken)
		resp, err = peerClient(t, testPeerToken).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("peer listener GET %s with the token: %d, want 200", p, resp.StatusCode)
		}
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

// A stop the leader answered 200 holds after the leader dies right away:
// the follower stored the entry and knows it is committed before the
// operator is told it is done, so the new leader, without a majority to
// catch up with, still applies it.
func TestPairAnsweredStopSurvivesTheLeaderDyingAtOnce(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	id := r.startedID(lead)
	pairWait(t, "the standby to apply the start", 5*time.Second, func() bool { return hasSubstitute(r.substitutes(stand), id) })
	// No heartbeat and no catch-up from here on: what the follower knows
	// is what the write itself told it.
	for _, n := range []string{"a", "b"} {
		r.nodes[n].m.logStop()
		<-r.nodes[n].m.logDone
	}
	r.nodes[lead].m.log.Ping(context.Background()) // a ping the stop cut short marked the peer lost
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, body)
	}
	r.nodes[lead].crash()
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	if hasSubstitute(r.substitutes(stand), id) {
		t.Fatalf("the operator was told substitute %d stopped, and the new leader still runs it", id)
	}
}
