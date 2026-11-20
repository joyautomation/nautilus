package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// A redundant pair member (docs/design/redundancy.md §2, §6.1): `naut run`
// with a manifest whose redundancy: names peers: and witness:. It elects
// through the quorum (leader.Quorum) instead of the Kubernetes Lease, and
// it answers its peer and the witness on a peer listener of its own,
// never behind the tag API's handler.
//
// What a member needs besides the manifest, from its environment (naut
// deploy's /etc/nautilus/env):
//
//   - NAUTILUS_NODE: a or b, which of the two controllers this one is
//     (set per box);
//   - NAUTILUS_PEER_TOKEN: the secret every peer request carries;
//   - NAUTILUS_CLUSTER: the pair's cluster ID, drawn when the pair is
//     commissioned and installed on both controllers and the witness. It
//     lives beside the peer token, outside the state directory, so a
//     controller whose state directory is wiped still starts and
//     re-adopts the pair's witness incarnation from its peer (#976);
//   - STATE_DIRECTORY (systemd's StateDirectory=) or redundancy:
//     state-dir:, where quorum.state is kept.
//
// A member missing any of them refuses to start: misconfiguration is
// never resolved quietly (§2).

// pairStateFile is a member's voter state, in its state directory.
const pairStateFile = "quorum.state"

// pairMaxConns caps a member's peer listener: its peer and the witness
// keep a few connections alive each.
const pairMaxConns = 64

// pairMember is a pair member's election and its peer listener.
type pairMember struct {
	q     *leader.Quorum
	srv   *http.Server
	o     node.Options
	peers map[string]string // the manifest's peers:, by name

	// The replicated log of operator intent, mounted on the peer listener
	// once the runtime exists (startLog); 503 until then.
	logH    swapHandler
	log     *oplog.Node
	al      *server.AlarmLog // nil without alarms
	rt      *runtime.Runtime
	logStop context.CancelFunc
	logDone chan struct{}
	// stuckTerm is the term whose catch-up last failed (driveLog), 0 once
	// it succeeds: the log is read-only under this leader (node.LogState).
	stuckTerm atomic.Uint64
	box       func() string // this box's fingerprint (BoxFunc), nil without
}

// swapHandler serves the handler it is given later, 503 before.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.h.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	http.Error(w, "the operator-intent log is not started yet", http.StatusServiceUnavailable)
}

// pairDeps are what newPairMember reads from the process: its
// environment and its listen call. Tests hand it their own.
type pairDeps struct {
	getenv func(string) string
	listen func(addr string) (net.Listener, error)
	// box is this box's fingerprint, given the addresses of the pair's
	// links (hostBox): an error refuses the start, "" means not readable
	// yet. dial, when set, opens the peer connections (tests cut links
	// with it).
	box  func(links []string) (string, error)
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// creds, when set, is this controller's credentials, read at every
	// hello (pairCredentials, #708).
	creds func() node.Credentials
}

func osPairDeps() pairDeps {
	return pairDeps{getenv: os.Getenv, listen: func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }, box: hostBox}
}

// machineIDFiles are where a box's machine-id is read from, in order.
var machineIDFiles = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// hostBox is this box's fingerprint for the voter state (#1029): its
// machine-id and the MACs of the interfaces the links to the peer and the
// witness leave by. Not the other NICs: a USB NIC plugged in, or a NIC
// the pair doesn't use, must not make a box "changed". With no machine-id
// it is an error (the start is refused); "" while a link's address
// doesn't resolve yet, and the controller then waits, neither voting nor
// campaigning, until it does (node.Options.BoxFunc): never a check
// skipped (#1034 review). That costs no liveness: it can't reach its
// peers without resolving them either.
func hostBox(links []string) (string, error) {
	var id string
	for _, f := range machineIDFiles {
		if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) != "" {
			id = string(b)
			break
		}
	}
	if id == "" {
		return "", fmt.Errorf("no machine-id (%s): a pair member binds its voter state to its box, so a cloned disk never votes as the original (#1029); give this box a machine-id (systemd-machine-id-setup)", strings.Join(machineIDFiles, ", "))
	}
	var macs []string
	for _, l := range links {
		mac, ok := linkMAC(l)
		if !ok {
			return "", nil
		}
		if mac != "" {
			macs = append(macs, mac)
		}
	}
	return node.BoxFingerprint(id, macs), nil
}

// linkMAC is the MAC of the interface a connection to addr would leave
// by ("" for one without, such as loopback). A UDP "dial" only picks the
// route; nothing is sent.
func linkMAC(addr string) (string, bool) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return "", false
	}
	local := c.LocalAddr().(*net.UDPAddr).IP
	c.Close()
	ifs, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, in := range ifs {
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(local) {
				return in.HardwareAddr.String(), true
			}
		}
	}
	return "", false
}

// newPairMember builds the election of a pair member and starts its peer
// listener. apiAddr is the tag API's bound address (its port is both
// controllers' API port: they run one manifest); dir is the project's
// checkout ("" for a built controller), which the state directory must not
// be inside. The election's Run is the caller's.
func newPairMember(r *project.RedundancyConfig, apiAddr, dir string, d pairDeps) (*pairMember, error) {
	o, listenAddr, err := pairOptions(r, apiAddr, dir, d.getenv)
	if err != nil {
		return nil, err
	}
	if d.box != nil {
		links := []string{o.PeerAddr, o.WitnessAddr}
		if _, err := d.box(links); err != nil {
			return nil, err
		}
		o.BoxFunc = func() string { b, _ := d.box(links); return b }
	}
	boxFn := o.BoxFunc
	o.Dial = d.dial
	o.Credentials = d.creds
	if err := os.MkdirAll(filepath.Dir(o.StatePath), 0o750); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	m := &pairMember{peers: r.Peers, box: boxFn}
	o.OnEvent = m.onEvent // events start with the election's Run, after m.q is set
	// The election first: it locks the state file, so a second process of
	// this controller stops here, before it binds anything.
	q, err := leader.NewQuorum(o)
	if err != nil {
		return nil, err
	}
	m.q, m.o = q, o
	// One listener, one token: the vote protocol and the operator-intent
	// log's replication (design §6.1). Nothing here is a browser surface.
	mux := http.NewServeMux()
	mux.Handle(node.Prefix, q.Node().Handler())
	for _, p := range []string{"/api/oplog", "/api/oplog/", "/api/quorum"} {
		mux.Handle(p, &m.logH)
	}
	h, err := node.Guard(o.Token, mux)
	if err != nil {
		q.Node().Close()
		return nil, err
	}
	ln, err := d.listen(listenAddr)
	if err != nil {
		q.Node().Close()
		return nil, fmt.Errorf("peer port %s: %w: is another controller (this one's service?) running?", listenAddr, err)
	}
	m.srv = peerServer(h)
	go func() {
		if err := m.srv.Serve(node.LimitListener(ln, pairMaxConns)); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("redundancy: the peer listener stopped: the peer and the witness no longer reach this controller's voter", "err", err)
		}
	}()
	return m, nil
}

// pairOptions resolves a member's elector options and the address its peer
// listener binds.
func pairOptions(r *project.RedundancyConfig, apiAddr, dir string, getenv func(string) string) (node.Options, string, error) {
	var o node.Options
	self := strings.ToLower(strings.TrimSpace(getenv("NAUTILUS_NODE")))
	switch self {
	case "a", "b":
	case "":
		return o, "", errors.New("NAUTILUS_NODE is not set: a pair member must say which controller it is, a or b as redundancy.peers names them (set it per box, beside NAUTILUS_PEER_TOKEN in /etc/nautilus/env under naut deploy)")
	default:
		return o, "", fmt.Errorf("NAUTILUS_NODE=%q: want a or b, as redundancy.peers names the pair's controllers", self)
	}
	peer := "b"
	if self == "b" {
		peer = "a"
	}
	token := getenv("NAUTILUS_PEER_TOKEN")
	if token == "" {
		return o, "", errors.New("NAUTILUS_PEER_TOKEN is not set: every peer request must carry it, so a pair member refuses to start without one (the same secret on both controllers and the witness)")
	}
	cluster := strings.TrimSpace(getenv("NAUTILUS_CLUSTER"))
	switch {
	case cluster == "":
		return o, "", errors.New("NAUTILUS_CLUSTER is not set: this pair is not commissioned. Commissioning draws the pair's cluster ID (32 hex digits) and sets NAUTILUS_CLUSTER beside NAUTILUS_PEER_TOKEN on both controllers and the witness (/etc/nautilus/env under naut deploy); the operator guide covers it")
	case !node.ValidCluster(cluster):
		return o, "", fmt.Errorf("NAUTILUS_CLUSTER=%q: want the 32 lowercase hex digits commissioning drew", cluster)
	}
	stateDir, err := pairStateDir(r, dir, getenv)
	if err != nil {
		return o, "", err
	}
	if err := checkAPIReachable(apiAddr, r.Peers[self]); err != nil {
		return o, "", err
	}
	_, peerPort, _ := net.SplitHostPort(r.Peers[self]) // Load checked the form
	o = node.Options{
		Cluster: cluster, Self: quorum.NodeID(self), Peer: quorum.NodeID(peer),
		PeerAddr: r.Peers[peer], WitnessAddr: r.Witness, Lease: r.LeaseTime(),
		NetDelay: time.Duration(r.NetDelay), SendMargin: time.Duration(r.SendMargin),
		StatePath: filepath.Join(stateDir, pairStateFile), Token: token,
		ClientToken: getenv("NAUTILUS_TOKEN"),
		// Compared at the hello: the section both must agree on, and what
		// this build does that the peer must too (#1013).
		Config:  node.ConfigHash(r.Peers["a"], r.Peers["b"], r.Witness),
		Caps:    []string{node.KeepsLiveIO},
		SelfAPI: apiOn(r.Peers[self], apiAddr), PeerAPI: apiOn(r.Peers[peer], apiAddr),
	}
	return o, net.JoinHostPort("", peerPort), nil
}

// checkAPIReachable refuses a tag API bound to loopback only while this
// member's peers: address is on the network: the peer could not reach it
// (a standby forwards writes to the leader's tag API, and /api/cluster
// advertises it). The manifest's default server.addr is localhost:8080,
// and naut deploy sets no address of its own.
func checkAPIReachable(apiAddr, selfPeer string) error {
	loop := func(host string) bool {
		ip := net.ParseIP(host)
		return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	}
	apiHost, _, err := net.SplitHostPort(apiAddr)
	if err != nil {
		return nil
	}
	peerHost, _, _ := net.SplitHostPort(selfPeer)
	if loop(apiHost) && !loop(peerHost) {
		return fmt.Errorf("the tag API listens on %s, loopback only, so the peer can't reach it (a standby forwards writes to the leader's tag API): set server.addr in the manifest, or NAUTILUS_ADDR in /etc/nautilus/env, to an address the peer reaches, such as :8080", apiAddr)
	}
	return nil
}

// pairStateDir is where a member keeps its voter state: redundancy:
// state-dir:, else $STATE_DIRECTORY (its first entry), never inside the
// project's checkout.
func pairStateDir(r *project.RedundancyConfig, dir string, getenv func(string) string) (string, error) {
	sd := r.StateDir
	if sd == "" {
		sd, _, _ = strings.Cut(getenv("STATE_DIRECTORY"), ":")
	}
	if sd == "" {
		return "", errors.New("no state directory for the voter state: run under systemd with StateDirectory= (naut deploy does), or set redundancy.state-dir to an absolute path outside the project")
	}
	sd, err := filepath.Abs(sd)
	if err != nil {
		return "", err
	}
	if dir != "" {
		proj, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if rel, err := filepath.Rel(proj, sd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("state directory %s is inside the project %s: a deploy replaces the project, and a lost voter state needs the peer or an operator to re-commission; set redundancy.state-dir outside it", sd, proj)
		}
	}
	return sd, nil
}

// apiOn is the tag API's address on a controller named by its peer
// address: its host, and the API port of apiAddr. Both controllers run
// one manifest, so one API port. "" when apiAddr has no port.
func apiOn(peerAddr, apiAddr string) string {
	host, _, err := net.SplitHostPort(peerAddr)
	_, port, perr := net.SplitHostPort(apiAddr)
	if err != nil || perr != nil || port == "" || port == "0" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

// onEvent shows the pair's conditions where an operator looks:
// /api/cluster's warnings and the dashboard's banner (leader.Status).
func (m *pairMember) onEvent(ev node.Event) {
	switch ev.Kind {
	case node.EventConfigDiffers:
		msg := ev.Detail
		if msg == "same" {
			msg = ""
		}
		m.q.SetCondition("config-differs", msg)
	case node.EventUsersDiffer:
		m.q.SetCondition("credentials-users", ev.Detail) // "" clears it
	case node.EventTLSDiffers:
		m.q.SetCondition("credentials-tls", ev.Detail)
	case node.EventYielded:
		m.q.SetCondition("oplog-yield", ev.Detail+" (at "+time.Now().UTC().Format(time.RFC3339)+")")
	}
}

// pairCredentials is what a pair member compares with its peer at every
// hello (#708, report only): the users file's digest, when the manifest
// names users (read anew each time, so an edit shows within one hello),
// and the tag API certificate's leaf (tlsSrc nil: no server.tls).
func pairCredentials(proj *project.Project, dir string, tlsSrc *server.CertSource, getenv func(string) string) func() node.Credentials {
	var path string
	var pathErr error
	if proj.Auth != nil && proj.Auth.HasUsers() {
		path, pathErr = usersFile(proj.AuthStateDir, proj.Name, dir, dir != "", getenv)
	}
	return func() node.Credentials {
		var c node.Credentials
		switch {
		case pathErr != nil:
			c.UsersErr = pathErr.Error()
		case path != "":
			d, err := users.Digest(path)
			if err != nil {
				c.UsersErr = err.Error()
			} else {
				c.Users = d
			}
		}
		if tlsSrc != nil {
			c.TLS = tlsSrc.Pin()
		}
		return c
	}
}

// peersKeepIO is the live I/O apply's mixed-version guard (#1013): ""
// once the peer has said at the hello that its build keeps a live I/O
// apply across a takeover, else why the apply must wait.
func (m *pairMember) peersKeepIO() string {
	caps, known := m.q.Node().PeerCaps()
	switch {
	case !known:
		return "the peer controller has not answered yet, so whether its build keeps a live I/O apply across a takeover is unknown (#1013): try again once it answers"
	case !slices.Contains(caps, node.KeepsLiveIO):
		return "the peer controller runs a build that does not keep a live I/O apply across a takeover (#1013): it would come up on the manifest's addresses and its next save would drop the apply. Finish the upgrade on both controllers, then apply"
	}
	return ""
}

// untrustedAPIs are the tag API addresses the pair advertises (/api/cluster,
// the standby's proxy) by a name the server won't answer a browser by:
// not localhost, not an IP, not in allowed (design §6.1). A browser HMI
// that follows the leader there gets 403.
func (m *pairMember) untrustedAPIs(allowed []string) []string {
	var out []string
	for _, a := range []string{m.o.SelfAPI, m.o.PeerAPI} {
		host, _, err := net.SplitHostPort(a)
		if err != nil || net.ParseIP(host) != nil || strings.EqualFold(host, "localhost") {
			continue
		}
		if !slices.ContainsFunc(allowed, func(h string) bool { return strings.EqualFold(h, host) }) {
			out = append(out, host)
		}
	}
	return out
}

// Close stops the peer listener, the log and frees the state file. Call it
// after the election's Run has returned and Release has run: the peer
// listener answers votes until then.
func (m *pairMember) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
	if m.logStop != nil {
		m.logStop()
		<-m.logDone
		m.log.Close()
	}
	m.q.Node().Close()
}

// pairLogFile is a member's copy of the operator-intent log, in its state
// directory beside quorum.state, and pairLogMark its mark (oplog.Mark:
// held, synced, the identity it belongs to, the highest term, a vouch).
const (
	pairLogFile = "oplog.jsonl"
	pairLogMark = "oplog.mark"
)

// PairNoMajority is a pair's no-majority refusal (#710 3b-4b): the log has
// three copies, A, B and the witness, and a write needs two.
const PairNoMajority = "operator-intent log has no majority: neither the other controller nor the witness answers; substitutes cannot be started, stopped or acknowledged, nor alarms acknowledged or shelved, until one of them returns"

// startLog builds the pair's replicated log of operator intent (oplog,
// design §1) over rt and mounts it on the peer listener: A, B and the
// witness are its replicas (#710 PR 3b-4b), so a write needs two of them.
// Only a synced replica counts (oplog.Mark), the witness's only at the
// incarnation the election counts.
// Order as #1017's wiring: the SubstituteLog, the node (which replays its
// file into Apply), Attach, then the runtime's SetIntentLog (a takeover
// catches the log up before its first scan, waiting up to wait). The
// returned log goes to the server's Options.SubstituteLog. Call it before
// the election's Run and the runtime's Run.
func (m *pairMember) startLog(rt *runtime.Runtime, wait time.Duration, eng *alarm.Engine) (*server.SubstituteLog, *server.AlarmLog, error) {
	sl := server.NewSubstituteLog(rt)
	sl.SetNoMajority(PairNoMajority)
	// Alarm acknowledgements and shelves (#710 PR 3b-2c): applied to the
	// live engine on the leader, to the replicated view on a standby; the
	// runtime restores the view at a takeover (PairAlarms).
	var al *server.AlarmLog
	if eng != nil {
		// Live while the election's gate is open, from the takeover's
		// start: an entry applied before the takeover's restore is reset
		// and restored with the view; one after it reaches the engine
		// (#1033 review).
		al = server.NewAlarmLog(eng, m.q.IsLeader, m.q.Term)
		rt.SetAlarms(server.PairAlarms{Engine: eng, Log: al})
	}
	m.al = al
	apply := func(e oplog.Entry) error {
		if e.Kind == oplog.KindLapse && al != nil {
			_, _ = al.Apply(e) // its view drops a lapsed ack or shelf; the substitutes' below
		}
		if done, err := sl.Apply(e); done {
			return err
		}
		if al != nil {
			_, err := al.Apply(e)
			return err
		}
		return nil
	}
	self := peerURL(m.peers[string(m.o.Self)])
	a, b, wit := peerURL(m.peers["a"]), peerURL(m.peers["b"]), witnessLogURL(m.o.WitnessAddr, m.o.Cluster)
	peers := []string{a, b, wit}
	if mutNaut("witness_not_a_replica") {
		peers = peers[:2]
	}
	dir := filepath.Dir(m.o.StatePath)
	file := filepath.Join(dir, pairLogFile)
	// A log kept from before marks (the 2-of-2 interim held every entry on
	// both controllers) counts as synced; no log at all (a new pair, a
	// wiped disk) does not, until the leader syncs it or a person vouches.
	_, statErr := os.Stat(file)
	_, markErr := os.Stat(filepath.Join(dir, pairLogMark))
	preMark := statErr == nil && errors.Is(markErr, os.ErrNotExist)
	n, err := oplog.New(oplog.Options{
		Self: self, Peers: peers,
		Leadership: pairLeadership{q: m.q, peers: m.peers},
		File:       file, Mark: filepath.Join(dir, pairLogMark),
		Identity: m.logIdentity(), UnmarkedSynced: preMark,
		Node:  string(m.o.Self),
		Names: map[string]string{a: "controller a", b: "controller b", wit: "the witness"},
		Counts: witnessCounts(wit, m.q.Node().CountedIncarnation),
		Apply: apply,
		Lapse: func(e oplog.Entry, now time.Time) bool {
			return sl.Lapse(e, now) || (al != nil && al.Lapse(e, now))
		},
		Token: m.o.Token, Timeout: max(m.o.Lease/2, 500*time.Millisecond),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("operator-intent log: %w", err)
	}
	m.rt = rt
	el := m.q.Node()
	el.SetLog(func() node.LogState {
		return node.LogState{Synced: n.Synced(), Stuck: m.stuck()}
	})
	el.SetVouch(func(ctx context.Context, by string) error {
		v, err := n.Vouch(ctx, by)
		if err == nil && v.By != "" {
			slog.Warn("redundancy: a person vouched for this controller's operator-intent log", "by", v.By, "lastTerm", v.LastTerm, "lastSeq", v.LastSeq)
		}
		return err
	})
	m.q.SetLogStatus(func() *leader.LogStatus { return m.logStatus() })
	sl.Attach(n)
	if al != nil {
		al.Attach(n)
	}
	rt.SetIntentLog(sl, wait)
	m.log = n
	h := n.Handler()
	m.logH.h.Store(&h)
	ctx, cancel := context.WithCancel(context.Background())
	m.logStop, m.logDone = cancel, make(chan struct{})
	// One ping before the server answers, so a write in the first second
	// is not refused for a peer merely not heard from yet.
	pctx, pcancel := context.WithTimeout(ctx, max(m.o.Lease, time.Second))
	n.Ping(pctx)
	pcancel()
	go m.driveLog(ctx)
	return sl, al, nil
}

// driveLog is the log's periodic work, every half lease: the leader's
// heartbeat (it carries the commit index, so the follower applies what is
// committed), the follower's catch-up, and the pings that keep its
// reachability (Status, the no-majority refusal) current.
func (m *pairMember) driveLog(ctx context.Context) {
	defer close(m.logDone)
	t := time.NewTicker(max(m.o.Lease/2, 250*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, max(m.o.Lease, time.Second))
		m.retrySync(cctx)
		m.log.Heartbeat(cctx)
		_ = m.log.CatchUp(cctx)
		m.log.Ping(cctx)
		cancel()
		if m.al != nil {
			m.al.Reconcile() // the leader's view holds only acks its engine holds
		}
		m.logConditions()
	}
}

// retrySync catches the leader's log up when its term is not synced (a
// takeover's catch-up failed, or a write missed its majority): nothing
// else would, since the witness never pulls, and the heartbeat sends
// nothing until then. Only once the runtime's takeover is done, so no
// entry is applied ahead of the takeover's restore (#1033). A failure
// marks the term stuck (node.LogState), which an unsynced leader yields
// on.
func (m *pairMember) retrySync(ctx context.Context) {
	if !m.q.IsLeader() || m.rt == nil || !m.rt.Leading() || m.log.TermSynced() || mutNaut("log_sync_not_retried") {
		if !m.q.IsLeader() {
			m.stuckTerm.Store(0)
		}
		return
	}
	term := m.q.Term()
	if err := m.log.SyncLeader(ctx); err != nil {
		if m.stuckTerm.Swap(term) != term {
			slog.Warn("redundancy: the operator-intent log can't catch up under this leader; it is read-only until it can", "term", term, "error", err)
		}
		return
	}
	m.stuckTerm.Store(0)
}

// stuck: this controller leads and its catch-up for the current term has
// failed.
func (m *pairMember) stuck() bool {
	t := m.stuckTerm.Load()
	return t != 0 && m.q.IsLeader() && t == m.q.Term() && !m.log.TermSynced()
}

// logConditions raises or clears the log's conditions on /api/cluster and
// the banner: a lost log, an unsynced one.
func (m *pairMember) logConditions() {
	st := m.log.Status()
	lost, unsynced := "", ""
	switch {
	case st.Lost != "":
		lost = "this controller's operator-intent log was lost (kept at " + st.Lost + "): it counts for nothing, and while this controller leads substitutes, alarm acknowledgements and shelves are refused, until it has synced from the other controller and the witness"
	case st.Unsynced:
		unsynced = "this controller's operator-intent log is not synced: it counts for nothing, and while this controller leads substitutes, alarm acknowledgements and shelves are refused, until it has synced from the other controller and the witness, or a person vouches for it (re-commission)"
	}
	m.q.SetCondition("oplog-lost", lost)
	m.q.SetCondition("oplog-unsynced", unsynced)
	if !st.Unsynced {
		m.q.SetCondition("oplog-yield", "")
	}
}

// logIdentity is this controller's log identity: cluster, node and box,
// so a log from a cloned or migrated disk is moved aside and synced
// again, never counted. It waits for the box while it can't be read yet
// (the elector waits for it too, so nothing is lost by it).
func (m *pairMember) logIdentity() string {
	box := m.o.Box
	if m.box != nil {
		for i := 0; ; i++ {
			if box = m.box(); box != "" {
				break
			}
			if i == 0 {
				slog.Warn("redundancy: this box's fingerprint can't be read yet (a peer or witness address doesn't resolve); the operator-intent log waits for it")
			}
			time.Sleep(time.Second)
		}
	}
	return m.o.Cluster + "/" + string(m.o.Self) + "/" + box
}

// logStatus is the log's block of /api/cluster.
func (m *pairMember) logStatus() *leader.LogStatus {
	if m.log == nil {
		return nil
	}
	st := m.log.Status()
	names := map[string]string{peerURL(m.peers["a"]): "a", peerURL(m.peers["b"]): "b", witnessLogURL(m.o.WitnessAddr, m.o.Cluster): "witness"}
	ls := &leader.LogStatus{Synced: !st.Unsynced, Lost: st.Lost, Writable: st.Writable, Reason: st.Reason, LastSeq: st.LastSeq, Commit: st.Commit}
	for _, p := range st.Peers {
		r := leader.LogReplica{Name: names[p], Reached: slices.Contains(st.Reached, p)}
		if p == st.Self {
			r.Synced, r.Self = !st.Unsynced, true
		} else {
			r.Synced = slices.Contains(st.Synced, p)
		}
		ls.Replicas = append(ls.Replicas, r)
	}
	if v := st.Vouch; v != nil {
		ls.Vouch = &leader.LogVouch{By: v.By, At: time.UnixMilli(v.At).UTC(), LastTerm: v.LastTerm, LastSeq: v.LastSeq}
	}
	return ls
}

// witnessCounts is the pair's log's Counts: the witness's copy counts only
// at the incarnation the election counts (its identity is that
// incarnation), so a rebuilt witness's log, synced or not, counts only
// once the pair has adopted it (F15). A controller's always may.
func witnessCounts(wit string, counted func() uint64) func(peer, identity string) bool {
	return func(peer, identity string) bool {
		if peer != wit || mutNaut("witness_log_any_incarnation") {
			return true
		}
		inc := counted()
		return inc != 0 && identity == strconv.FormatUint(inc, 10)
	}
}

// witnessLogURL is the witness's copy of the cluster's log, as the pair's
// log names it: on the witness's peer listener, under the cluster.
func witnessLogURL(witness, cluster string) string {
	base := witness
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return strings.TrimRight(base, "/") + node.Prefix + cluster + "/log"
}

// pairLeadership is the log's view of the pair's election: the sequencer
// is the quorum's leader, addressed by its peer listener exactly as Peers
// names it, and the log's term is the quorum's (it only rises).
type pairLeadership struct {
	q     *leader.Quorum
	peers map[string]string
}

func (l pairLeadership) IsLeader() bool { return l.q.IsLeader() }
func (l pairLeadership) Term() uint64   { return l.q.Term() }
func (l pairLeadership) LeaderAddr() string {
	// The elector's own status, never the Quorum's: that one reads the
	// log's status block, which asks this.
	if a, ok := l.peers[l.q.Node().Status().Leader]; ok {
		return peerURL(a)
	}
	return ""
}

// peerURL is a peer listener's base URL, as the log names its replicas.
func peerURL(addr string) string { return "http://" + addr }

// peerServer serves a peer listener (a pair member's, the witness's): a
// vote is one small frame, so a slow or idle client is cut off.
func peerServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 8 << 10}
}
