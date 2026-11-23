package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/internal/fence"
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
	q   *leader.Quorum
	srv *http.Server
	// bindStop ends a late bind (bindLater); bindDone closes once the peer
	// port is bound or the late bind has stopped.
	bindStop, bindDone chan struct{}
	closeOnce          sync.Once
	o                  node.Options
	peers              map[string]string // the manifest's peers:, by name

	// The replicated log of operator intent, mounted on the peer listener
	// once the runtime exists (startLog); 503 until then.
	logH swapHandler
	log  *oplog.Node
	// The state stream (#710 S1b, pairstream.go), mounted on the peer
	// listener once the runtime exists (startStream); 503 until then.
	streamH    swapHandler
	stream     *pairStream
	streamStop context.CancelFunc
	streamDone chan struct{}
	al         *server.AlarmLog // nil without alarms
	nl         *server.NotesLog // the pair's operator notes (#720)
	// cl is the pair's I/O checkout (#719 slice 5): runs in the log. Made by
	// startLog only when wantCheckout was set (the project has a checkout
	// section and auth users), so a project without one gets no key file.
	// cl is set once in startLog, before the log node starts, so nothing that
	// reads it (endSuperseded, the duties) can run before it is set.
	cl           *server.CheckoutLog
	wantCheckout bool
	sess         *server.SessionLog // the pair's browser sessions (#716)
	rt           *runtime.Runtime
	logStop      context.CancelFunc
	logDone      chan struct{}
	logPending   atomic.Bool // the log opened before the box could be read (logIdentity)
	// stuckTerm is the term whose catch-up last failed (driveLog), 0 once
	// it succeeds: the log is read-only under this leader (node.LogState).
	stuckTerm atomic.Uint64
	box       func() string // this box's fingerprint (BoxFunc), nil without
	// witnessLease: the witness refuses this controller's lease
	// (WitnessLeaseCondition), for the system alarm.
	witnessLease atomic.Bool

	// The supersede self-restart (#1029 3c-3c-2a, ruling 20d): getenv
	// tells a supervisor (INVOCATION_ID, NOTIFY_SOCKET); restart closes
	// once, asking naut run to exit for the supervisor to start it again;
	// journal records it (SetJournal).
	getenv      func(string) string
	restart     chan struct{}
	restartOnce sync.Once
	journal     atomic.Pointer[audit.Recorder]
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
	// bindEvery is how often a peer listener whose address is not up yet
	// tries again (0: bindEveryDefault).
	bindEvery time.Duration
}

// bindEveryDefault is how often a member retries binding its peer port
// while the address is not this box's yet (a boot whose network or DNS is
// late, a cable out). It never gives up and never exits for it: a start
// refused for a late network would spend the unit's start limit and leave
// the controller down for good (#1230 review).
const bindEveryDefault = time.Second

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
	o, listenAddr, srvTLS, err := pairOptions(r, apiAddr, dir, d.getenv)
	if err != nil {
		return nil, err
	}
	// What an operator edits to move the peer port: the override, else
	// this controller's peers: entry.
	listenKey := "redundancy.peers." + string(o.Self)
	if strings.TrimSpace(d.getenv("NAUTILUS_PEER_LISTEN")) != "" {
		listenKey = "NAUTILUS_PEER_LISTEN"
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
	m := &pairMember{peers: r.Peers, box: boxFn, getenv: d.getenv, restart: make(chan struct{})}
	o.OnEvent = m.onEvent // events start with the election's Run, after m.q is set
	// The election first: it locks the state file, so a second process of
	// this controller stops here, before it binds anything.
	q, err := leader.NewQuorum(o)
	if err != nil {
		return nil, err
	}
	m.q, m.o = q, o
	if srvTLS == nil {
		// Loud at every start, and standing on /api/cluster (#710).
		slog.Warn("redundancy: " + peerTLSOff)
		q.Warn(peerTLSOff)
	}
	// One listener, one token: the vote protocol and the operator-intent
	// log's replication (design §6.1). Nothing here is a browser surface.
	mux := http.NewServeMux()
	mux.Handle(node.Prefix, q.Node().Handler())
	for _, p := range []string{"/api/oplog", "/api/oplog/", "/api/quorum"} {
		mux.Handle(p, &m.logH)
	}
	mux.Handle(streamPath(o.Cluster), &m.streamH)
	h, err := node.Guard(o.Token, spareSilent(q.Node().Spare, mux))
	if err != nil {
		q.Node().Close()
		return nil, err
	}
	m.srv = peerServer(h)
	m.bindStop, m.bindDone = make(chan struct{}), make(chan struct{})
	ln, err := d.listen(listenAddr)
	var later string // why the bind waits (the peer-bind condition)
	switch {
	case err == nil:
		if why := loopbackSurprise(listenAddr, ln.Addr(), listenKey); why != "" {
			// Not refused: a name can resolve to loopback only until the
			// network is up (systemd-resolved with no carrier), and a
			// refused start would spend the start limit (#1230 re-check).
			ln.Close()
			slog.Error("redundancy: " + why)
			later = why
			break
		}
		m.serve(ln, srvTLS)
		close(m.bindDone)
	case notHere(err) && !mutNaut("peer_bind_gives_up"):
		later = notBoundMsg(listenAddr, listenKey, err)
		slog.Warn("redundancy: " + later)
	default:
		q.Node().Close()
		if notHere(err) {
			return nil, fmt.Errorf("peer port %s: %w: %s must name an address of this box", listenAddr, err, listenKey)
		}
		return nil, fmt.Errorf("peer port %s: %w: is another controller (this one's service?) running?", listenAddr, err)
	}
	if later != "" {
		// The address is not usable yet: keep trying, never exit.
		m.q.SetCondition("peer-bind", later)
		every := d.bindEvery
		if every <= 0 {
			every = bindEveryDefault
		}
		go m.bindLater(d.listen, listenAddr, listenKey, srvTLS, every)
	}
	return m, nil
}

// serve serves the peer listener on ln, with peer TLS when srvTLS is set.
func (m *pairMember) serve(ln net.Listener, srvTLS *tls.Config) {
	slog.Info("redundancy: peer port bound", "addr", ln.Addr().String(), "tls", srvTLS != nil)
	served := node.LimitListener(ln, pairMaxConns)
	if srvTLS != nil {
		served = tls.NewListener(served, srvTLS)
	}
	go func() {
		if err := m.srv.Serve(served); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("redundancy: the peer listener stopped: the peer and the witness no longer reach this controller's voter", "err", err)
		}
	}()
}

// bindLater binds the peer port once its address is this box's, trying
// every every until Close; the peer-bind condition stands until then.
func (m *pairMember) bindLater(listen func(string) (net.Listener, error), addr, key string, srvTLS *tls.Config, every time.Duration) {
	defer close(m.bindDone)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-m.bindStop:
			return
		case <-t.C:
		}
		ln, err := listen(addr)
		if err != nil {
			m.q.SetCondition("peer-bind", notBoundMsg(addr, key, err))
			continue
		}
		if why := loopbackSurprise(addr, ln.Addr(), key); why != "" {
			ln.Close()
			m.q.SetCondition("peer-bind", why)
			continue
		}
		m.serve(ln, srvTLS)
		m.q.SetCondition("peer-bind", "")
		return
	}
}

// notBoundMsg is the peer-bind condition while the address is not up.
func notBoundMsg(addr, key string, err error) string {
	return fmt.Sprintf("the peer port %s is not bound yet (%v): until it is, the other controller and the witness can't reach this controller's voter. naut run keeps trying; if the address never comes up, %s must name an address of this box", addr, err, key)
}

// notHere reports a bind that failed because the address is not (yet)
// this box's: not configured on any interface, or a name that does not
// resolve.
func notHere(err error) bool {
	var dns *net.DNSError
	return errors.Is(err, syscall.EADDRNOTAVAIL) || errors.As(err, &dns)
}

// loopbackSurprise is why a bind of configured (a host name, not an IP)
// that landed on a loopback address is refused: the peer and the witness
// could never reach it (a stock /etc/hosts maps the box's own name to
// 127.0.1.1). "" when configured is an IP or empty, or bound is not
// loopback.
func loopbackSurprise(configured string, bound net.Addr, key string) string {
	host, _, err := net.SplitHostPort(configured)
	if err != nil || host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return ""
	}
	tcp, ok := bound.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() || mutNaut("peer_bind_loopback_taken") {
		return ""
	}
	return fmt.Sprintf("%s names %s, which resolves to %s on this box, a loopback address the other controller and the witness can't reach (check /etc/hosts): name this box's network address (an IP), or set NAUTILUS_PEER_LISTEN", key, host, tcp.IP)
}

// pairOptions resolves a member's elector options, the address its peer
// listener binds and the TLS it serves there (nil: peer TLS off).
func pairOptions(r *project.RedundancyConfig, apiAddr, dir string, getenv func(string) string) (node.Options, string, *tls.Config, error) {
	var o node.Options
	self := strings.ToLower(strings.TrimSpace(getenv("NAUTILUS_NODE")))
	switch self {
	case "a", "b":
	case "":
		return o, "", nil, errors.New("NAUTILUS_NODE is not set: a pair member must say which controller it is, a or b as redundancy.peers names them (set it per box, beside NAUTILUS_PEER_TOKEN in /etc/nautilus/env under naut deploy)")
	default:
		return o, "", nil, fmt.Errorf("NAUTILUS_NODE=%q: want a or b, as redundancy.peers names the pair's controllers", self)
	}
	peer := "b"
	if self == "b" {
		peer = "a"
	}
	token := getenv("NAUTILUS_PEER_TOKEN")
	if token == "" {
		return o, "", nil, errors.New("NAUTILUS_PEER_TOKEN is not set: every peer request must carry it, so a pair member refuses to start without one (the same secret on both controllers and the witness)")
	}
	cluster := strings.TrimSpace(getenv("NAUTILUS_CLUSTER"))
	switch {
	case cluster == "":
		return o, "", nil, errors.New("NAUTILUS_CLUSTER is not set: a new pair's cluster ID (32 hex digits) and peer token are drawn by `naut redundancy new-pair`, which writes each box's env lines (/etc/nautilus/env under naut deploy); then start all three and run `naut redundancy commission`. The operator guide covers it")
	case !node.ValidCluster(cluster):
		return o, "", nil, fmt.Errorf("NAUTILUS_CLUSTER=%q: want the 32 lowercase hex digits naut redundancy new-pair drew", cluster)
	}
	stateDir, err := pairStateDir(r, dir, getenv)
	if err != nil {
		return o, "", nil, err
	}
	if err := checkAPIReachable(apiAddr, r.Peers[self]); err != nil {
		return o, "", nil, err
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
		Config: node.ConfigHash(r.Peers["a"], r.Peers["b"], r.Witness),
		Caps:   []string{node.KeepsLiveIO},
		// The peer's tag API is the one its hello announces, never this
		// port on its host: the two need not match (#710 demo finding 1).
		SelfAPI: apiOn(r.Peers[self], apiAddr),
	}
	pt, err := buildPeerTLS(token, peerTLSIsOff(r.PeerTLS), "redundancy.peer-tls: off")
	if err != nil {
		return o, "", nil, err
	}
	var srv *tls.Config
	if pt != nil {
		o.TLS, srv = pt.Client, pt.Server
	}
	// The address peers: names, not every interface: a peer port reachable
	// from networks that don't need it (a VPN, a second NIC) is exposure
	// for nothing, and two controllers on one host can share a port number
	// on two addresses.
	listen := r.Peers[self]
	if mutNaut("peer_bind_wildcard") {
		listen = net.JoinHostPort("", peerPort)
	}
	// NAUTILUS_PEER_LISTEN, per box, binds another address: a controller
	// behind NAT or in a container with mapped ports, whose peers: entry
	// is where the others reach it, not an address it has.
	if v := strings.TrimSpace(getenv("NAUTILUS_PEER_LISTEN")); v != "" {
		_, port, err := net.SplitHostPort(v)
		if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 {
			return o, "", nil, fmt.Errorf("NAUTILUS_PEER_LISTEN=%q: want host:port (or :port for every address) for this controller's peer port", v)
		}
		listen = v
	}
	return o, listen, srv, nil
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
	case node.EventContactsDiffer:
		m.q.SetCondition("alarm-contacts", ev.Detail) // report only (#715)
	case node.EventVouchRefused:
		m.q.SetCondition("oplog-vouch-refused", ev.Detail)
	case node.EventYielded:
		m.q.SetCondition("oplog-yield", ev.Detail+" (at "+time.Now().UTC().Format(time.RFC3339)+")")
	case node.EventSpare:
		m.q.SetCondition("spare", ev.Detail)
	case node.EventPeerTLS:
		m.q.SetCondition("peer-tls-mismatch/"+string(ev.Voter), ev.Detail) // "" clears it (#710)
	case node.EventPeerIdentity:
		m.q.SetCondition("peer-identity/"+string(ev.Voter), ev.Detail)
	case node.EventLeaseMismatch:
		m.leaseMismatch(ev)
	}
}

// WitnessLeaseCondition is the condition (and the system alarm's source)
// raised while the witness refuses this controller's votes for their
// lease: the pair runs on its two controllers alone (#710).
const WitnessLeaseCondition = "witness-lease"

// leaseMismatch shows a lease refusal between this controller and a voter
// as a condition, cleared once the lease is taken again (#710): a voter's
// refusal of this controller's lease, and this controller's own voter's
// refusal of the peer's. Both sides show it, since during an upgrade the
// refused side may run a release that shows nothing.
func (m *pairMember) leaseMismatch(ev node.Event) {
	if mutNaut("lease_condition_dropped") {
		return
	}
	lease := m.o.Lease
	if lease == 0 {
		lease = node.DefaultLease
	}
	switch {
	case ev.Voter == m.witnessID() && !ev.Inbound:
		msg := ""
		if ev.Detail != "" {
			msg = fmt.Sprintf("no fault tolerance: the witness refuses this controller's votes (%s), so this controller leads or takes over only with its peer's vote. Start the witness with --lease %s or longer (this pair's redundancy.lease; a witness that serves several pairs takes the longest), or give the pair a lease no longer than the witness's", ev.Detail, lease)
		}
		m.witnessLease.Store(msg != "")
		m.q.SetCondition(WitnessLeaseCondition, msg)
	case ev.Voter == m.o.Peer && !ev.Inbound:
		msg := ""
		if ev.Detail != "" {
			msg = fmt.Sprintf("controller %s refuses this controller's votes (%s): the two run different leases, so this controller leads or takes over only with the witness's vote. Give both the same redundancy.lease", ev.Voter, ev.Detail)
		}
		m.q.SetCondition("lease-mismatch/"+string(ev.Voter), msg)
	case ev.Voter == m.o.Peer:
		msg := ""
		if ev.Detail != "" {
			msg = fmt.Sprintf("this controller refuses controller %s's votes (%s): the two run different leases, so %s leads or takes over only with the witness's vote. While a pair is upgraded across the change of the default lease (2 s to 1 s), this stands until %s runs the new release; otherwise give both the same redundancy.lease", ev.Voter, ev.Detail, ev.Voter, ev.Voter)
		}
		m.q.SetCondition("lease-refused/"+string(ev.Voter), msg)
	}
}

// witnessID is the witness's name in the vote protocol.
func (m *pairMember) witnessID() quorum.NodeID {
	if m.o.Witness != "" {
		return m.o.Witness
	}
	return node.WitnessID
}

// WitnessLeaseRefused reports whether the witness refuses this
// controller's votes for their lease (WitnessLeaseCondition): one atomic
// load, for the system alarm's every evaluation.
func (m *pairMember) WitnessLeaseRefused() bool { return m.witnessLease.Load() }

// spareSilent answers every request on the peer listener 503 while this
// controller is a spare (#1029 3c-2): the vote protocol and the
// operator-intent log's replication alike, as a stopped controller would.
func spareSilent(spare func() bool, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if spare() && !mutNaut("spare_log_runs") {
			http.Error(w, "not running: this controller is a spare", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	})
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
	// The alarm contacts and channel secrets (#715), when a channel pages
	// people: digested under the peer token (oncall.Digest), so the digest
	// says nothing about a weak password to anyone without it.
	var contactsDir string
	var contactsErr error
	if proj.Alarms != nil && len(proj.Alarms.Channels.Kinds()) > 0 {
		contactsDir, contactsErr = controllerStateDir("the alarm contacts", proj.AuthStateDir, proj.Name, dir, dir != "", getenv)
	}
	peerKey := []byte(getenv("NAUTILUS_PEER_TOKEN"))
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
		switch {
		case contactsErr != nil:
			c.ContactsErr = contactsErr.Error()
		case contactsDir != "":
			d, err := oncall.Digest(contactsDir, peerKey)
			if err != nil {
				c.ContactsErr = err.Error()
			} else {
				c.Contacts = d
			}
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

// untrustedAPIs are the hosts of the tag APIs the pair advertises
// (/api/cluster, the standby's proxy) by a name the server won't answer a
// browser by: not localhost, not an IP, not in allowed (design §6.1). A
// browser HMI that follows the leader there gets 403. Each controller's tag
// API is on the host peers: names for it (apiOn); the peer's port is the
// one its hello announces, but the host is all this check needs.
func (m *pairMember) untrustedAPIs(allowed []string) []string {
	var out []string
	for _, a := range []string{m.o.SelfAPI, m.peers[string(m.o.Peer)]} {
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
	if m.bindStop != nil {
		m.closeOnce.Do(func() { close(m.bindStop) })
		<-m.bindDone
	}
	// The state stream first: its handlers end with it, so the shutdown
	// below does not wait on a stream that never ends by itself.
	if m.streamStop != nil {
		m.streamStop()
		<-m.streamDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
	if m.logStop != nil {
		m.logStop()
		<-m.logDone
		m.log.Close()
		if m.sess != nil {
			m.sess.Close()
		}
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

// OplogReadOnly is the condition of a synced leader that can't sync: no
// other copy counts (both lost or rebuilt, or silent), and no vouch is
// pending.
const OplogReadOnly = "operator log read-only: no second counting copy; if the other controller and the witness can't be brought back, a person accepts what they may hold: naut redundancy vouch (it prints what would be lost), then naut redundancy vouch --accept-loss=<digest>"

// OplogReadOnlyNewPair is OplogReadOnly on a copy whose log is empty at
// history 0, as a new pair's is when a controller restarted before its
// first catch-up (#1029 3c-3c-2b, S2): with every copy answering and
// every log empty, the plain vouch is enough.
const OplogReadOnlyNewPair = "operator log read-only: no second counting copy; this log is empty, as a new pair's is when a controller restarted before its first catch-up: with the other controller and the witness answering and their logs empty too, a person runs: naut redundancy vouch (no flags: no copy shows anything to lose; if it refuses, it names why)"

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
func (m *pairMember) startLog(rt *runtime.Runtime, wait time.Duration, eng *alarm.Engine) (*server.SubstituteLog, *server.AlarmLog, *server.NotesLog, error) {
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
	// Operator notes (#720): the log is their store; the index is rebuilt
	// from it by replay, here, and emptied when a history is superseded.
	nl := server.NewNotesLog()
	m.nl = nl
	var cl *server.CheckoutLog
	if m.wantCheckout {
		var cerr error
		if cl, cerr = server.NewCheckoutLog(filepath.Dir(m.o.StatePath)); cerr != nil {
			return nil, nil, nil, fmt.Errorf("the I/O checkout: %w", cerr)
		}
		m.cl = cl
	}
	sess := server.NewSessionLog()
	m.sess = sess
	sess.SetFile(filepath.Join(filepath.Dir(m.o.StatePath), "session-ends.json"))
	apply := func(e oplog.Entry) error {
		if done, err := nl.Apply(e); done {
			return err
		}
		if cl != nil {
			if done, err := cl.Apply(e); done {
				return err
			}
		}
		if done, err := sess.Apply(e); done {
			return err
		}
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
	identity, pending := m.logIdentity(filepath.Join(dir, pairLogMark))
	n, err := oplog.New(oplog.Options{
		Self: self, Peers: peers,
		Leadership: pairLeadership{q: m.q, peers: m.peers},
		File:       file, Mark: filepath.Join(dir, pairLogMark),
		Identity: identity, Pending: pending, UnmarkedSynced: preMark,
		Node:   string(m.o.Self),
		Names:  map[string]string{a: "controller a", b: "controller b", wit: "the witness"},
		Counts: witnessCounts(wit, m.q.Node().CountedIncarnation),
		Apply:  apply,
		// the log started empty in this process (a rebind to another box): so do its notes
		OnEmptied: func() {
			nl.EndSuperseded()
			if cl != nil {
				cl.EndSuperseded()
			}
		},
		Lapse: func(e oplog.Entry, now time.Time) bool {
			return sl.Lapse(e, now) || (al != nil && al.Lapse(e, now))
		},
		Token: m.o.Token, TLS: m.o.TLS, Timeout: max(m.o.Lease/2, 500*time.Millisecond),
		// A copy superseded by another history (#1029 3c-3c-2a): the
		// runtime ends the old history's substitutes at once, leading or
		// not (driveLog repeats it).
		OnSuperseded: func(oplog.Lineage) { m.endSuperseded() },
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("operator-intent log: %w", err)
	}
	if err := dropStaleRetained(n, rt); err != nil {
		n.Close()
		return nil, nil, nil, fmt.Errorf("operator-intent log: %w", err)
	}
	m.logPending.Store(pending)
	m.rt = rt
	el := m.q.Node()
	el.SetLog(func() node.LogState {
		own, _ := n.History()
		return node.LogState{Synced: n.Synced(), Stuck: m.stuck(), CanSync: n.CanSync(m.o.Lease), History: own.History, HistoryID: own.HistoryID}
	})
	el.SetVouch(func(ctx context.Context, by string, inc uint64, acc node.LogAccept) (json.RawMessage, error) {
		// The witness's copy counts at the incarnation being adopted.
		v, pic, err := n.Vouch(ctx, by, witnessCounts(wit, func() uint64 { return inc }), oplog.VouchAccept{Loss: acc.Loss, LostLog: acc.LostLog})
		if err == nil && v.By != "" {
			slog.Warn("redundancy: a person vouched for this controller's operator-intent log", "by", v.By, "lastTerm", v.LastTerm, "lastSeq", v.LastSeq, "accepted", pic.Needs, "digest", pic.Digest)
		}
		var raw json.RawMessage
		if pic.Digest != "" {
			raw, _ = json.Marshal(pic)
		}
		return raw, err
	})
	m.q.SetLogStatus(func() *leader.LogStatus { return m.logStatus() })
	m.q.SetWritesCut(fence.CutCounts)
	sl.Attach(n)
	nl.Attach(n)
	if cl != nil {
		cl.Attach(n)
	}
	sess.Attach(n)
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
	return sl, al, nl, nil
}

// dropStaleRetained drops what the retain store keeps of a superseded
// log's intent (substitutes, acks, shelves), once its start moved that log aside (#1029 3c-3c-2a,
// ruling 20d), before the runtime runs: their IDs are the old history's
// seqs. The log's mark says it is owed until done (Mark.DropRetained), so
// a start that dies in between drops them at the next. An error refuses
// the start: running with them would restore them at a takeover.
func dropStaleRetained(n *oplog.Node, rt *runtime.Runtime) error {
	if _, to := n.History(); to.History != 0 || !n.RetainedStale() || mutNaut("stale_retained_kept") {
		return nil // superseding (not yet moved aside), or nothing owed
	}
	k, err := rt.DropRetainedIntent()
	if err != nil {
		return fmt.Errorf("dropping a superseded log's intent from the retain store: %w", err)
	}
	if k > 0 {
		slog.Warn("redundancy: dropped the superseded operator-intent log's substitutes, acks and shelves from the retain store", "entries", k)
	}
	return n.RetainedDropped()
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
		if m.q.Node().Spare() && !mutNaut("spare_log_runs") {
			continue // a spare neither replicates nor syncs (#1029 3c-2)
		}
		m.rebindLog()
		m.learnPeerHistory()
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
		m.endSuperseded()
		m.supersedeRestart()
	}
}

// endSuperseded ends every substitute the runtime holds while this
// controller's log is superseded (#1029 3c-3c-2a): they are the old
// history's. A leader keeps leading for control, read-only for operator
// intent, and hands over to a synced controller at the new history that
// may campaign (maybeYield).
func (m *pairMember) endSuperseded() {
	if m.log == nil || m.rt == nil || mutNaut("superseded_lead_keeps_substitutes") {
		return
	}
	if _, to := m.log.History(); to.History != 0 {
		m.rt.EndSupersededSubstitutes()
		if m.al != nil {
			m.al.EndSuperseded()
		}
		if m.nl != nil {
			m.nl.EndSuperseded() // its notes are now the new history's
		}
		if m.cl != nil {
			m.cl.EndSuperseded() // and its checkout runs
		}
	}
}

// learnPeerHistory takes a higher log history the other controller's
// hello reports (authenticated by the pair's MAC), so this copy stops
// counting from first contact, not only once replication meets it (#1029
// 3c-3c-2a).
func (m *pairMember) learnPeerHistory() {
	pl, ok := m.q.Node().PeerLog()
	learnFromHello(m.log, pl, ok)
}

// historyLearner is the log as learnFromHello uses it.
type historyLearner interface {
	History() (own, to oplog.Lineage)
	LearnHistory(oplog.Lineage) error
}

// learnFromHello takes a higher history the other controller's last
// hello reported (heard: within the last lease).
func learnFromHello(log historyLearner, pl node.PeerLog, heard bool) {
	if !heard || mutNaut("hello_history_ignored") {
		return
	}
	if own, _ := log.History(); pl.History > own.History {
		if err := log.LearnHistory(oplog.Lineage{History: pl.History, HistoryID: pl.HistoryID}); err != nil {
			slog.Error("redundancy: can't record the newer operator-intent log history the other controller reports", "error", err)
		}
	}
}

// supersedeWhy is why a superseded controller does not restart itself
// (supersedeRestart), "" when it may: it does not lead; the other
// controller leads, synced, at the history it takes; a supervisor starts
// it again; and this start did not already move a superseded log aside
// (once per mark, so it never loops: a person decides a second time).
func (m *pairMember) supersedeWhy(to oplog.Lineage) string {
	pl, ok := m.q.Node().PeerLog()
	leading := m.q.IsLeader() || m.rt != nil && m.rt.Leading()
	supervised := m.getenv("INVOCATION_ID") != "" || m.getenv("NOTIFY_SOCKET") != ""
	return supersedeRule(leading, pl, ok, to, supervised, m.log.SupersededAtStart())
}

// supersedeRule is supersedeWhy's decision (ruling 20d), "" when the
// controller restarts itself.
func supersedeRule(leading bool, pl node.PeerLog, heard bool, to oplog.Lineage, supervised, restartedForOne bool) string {
	switch {
	case leading && !mutNaut("restart_while_leading"):
		return "it leads: it never restarts itself while leading; restart naut on it once the other controller leads"
	case (!heard || !pl.Leads || !pl.Synced || pl.History != to.History || pl.HistoryID != to.HistoryID) && !mutNaut("restart_peer_unchecked"):
		return "the other controller does not lead synced at that history yet; it restarts itself once it does"
	case !supervised && !mutNaut("restart_unsupervised"):
		return "no supervisor would start it again (not run by systemd): restart naut on it"
	case restartedForOne && !mutNaut("restart_loops"):
		return "it already restarted once for a superseded log since it started: restart naut on it"
	}
	return ""
}

// supersedeRestart raises the superseded condition and, under ruling 20d,
// restarts this controller once: its log can be moved aside, before it is
// replayed, only at a start (the runtime holds what the old history
// applied). Journalled as redundancy.oplog-superseded-restart.
func (m *pairMember) supersedeRestart() {
	own, to := m.log.History()
	if to.History == 0 {
		m.q.SetCondition("oplog-superseded", "")
		return
	}
	who := to.HistoryBy
	if who == "" {
		who = "a person"
	}
	msg := fmt.Sprintf("this controller's operator-intent log was superseded by history %d (a loss %s accepted on another copy): it counts for nothing until naut restarts here, which moves the old log aside and syncs the new one", to.History, who)
	why := m.supersedeWhy(to)
	if why != "" {
		m.q.SetCondition("oplog-superseded", msg+"; "+why)
		return
	}
	m.q.SetCondition("oplog-superseded", msg+"; restarting now")
	m.restartOnce.Do(func() {
		slog.Error("redundancy: restarting naut to take the operator-intent log's newer history", "history", own.History, "to", to.History, "acceptedBy", to.HistoryBy)
		if j := m.journal.Load(); j != nil {
			_, _ = (*j).Record(audit.Entry{
				User: "system", Source: audit.SourceSystem,
				Action: "redundancy.oplog-superseded-restart", Target: string(m.o.Self),
				Detail: map[string]any{"history": own.History, "to": to.History, "toId": fmt.Sprintf("%x", to.HistoryID), "acceptedBy": to.HistoryBy,
					"why": "standby, the other controller leads synced at the new history, under a supervisor: exits for the supervisor to start it again, moving the superseded log aside before it is replayed"},
				Result: audit.ResultOK,
			})
		}
		close(m.restart)
	})
}

// RestartRequested is closed when this controller asks naut run to exit
// for its supervisor to start it again (supersedeRestart).
func (m *pairMember) RestartRequested() <-chan struct{} { return m.restart }

// SetJournal gives the pair the audit journal its own actions go to.
func (m *pairMember) SetJournal(j audit.Recorder) {
	if j != nil {
		m.journal.Store(&j)
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
		lost = "this controller's operator-intent log was lost (kept at " + st.Lost + "): it counts for nothing, and while this controller leads substitutes, alarm acknowledgements and shelves are refused, until it has synced from the other controller and the witness; if neither holds the log, a person accepts its loss: naut redundancy vouch, then naut redundancy vouch --accept-lost-log=<digest>"
	case st.Unsynced && st.SupersedeTo == 0:
		unsynced = "this controller's operator-intent log is not synced: it counts for nothing, and while this controller leads substitutes, alarm acknowledgements and shelves are refused, until it has synced from the other controller and the witness, or a person vouches for it (naut redundancy vouch)"
	}
	m.q.SetCondition("oplog-lost", lost)
	m.q.SetCondition("oplog-unsynced", unsynced)
	forked := ""
	for _, f := range st.Forked {
		if forked != "" {
			forked += "; "
		}
		forked += fmt.Sprintf("operator log forked at history %d (accepted by %s here, %s on %s): choose one with naut redundancy vouch on the controller whose log you keep", f.History, nonEmpty(f.Ours, "a person"), nonEmpty(f.Theirs, "a person"), f.Peer)
	}
	m.q.SetCondition("oplog-forked", forked)
	readOnly := ""
	if !st.Unsynced && m.stuck() && !m.log.CanSync(m.o.Lease) {
		// Synced, leading, but alone among the copies that count: entries
		// committed while it lagged may be on the others, so only a person
		// can decide (#1053).
		readOnly = OplogReadOnly
		if st.LastSeq == 0 && st.History == 0 {
			readOnly = OplogReadOnlyNewPair
		}
	}
	m.q.SetCondition("oplog-read-only", readOnly)
	if !st.Unsynced {
		m.q.SetCondition("oplog-yield", "")
		m.q.SetCondition("oplog-vouch-refused", "")
	}
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

// logIdentity is this controller's log identity: cluster, node and box,
// so a log from a cloned or migrated disk is moved aside and synced
// again, never counted. While the box can't be read yet (the elector
// holds then too), the log opens provisionally under the identity its mark
// names (or logPlaceholder), counting for nothing, and driveLog re-binds
// it once the box reads (rebindLog).
func (m *pairMember) logIdentity(mark string) (identity string, pending bool) {
	box := m.o.Box
	if m.box != nil {
		box = m.box()
	}
	if box != "" || m.box == nil {
		return m.logPrefix() + box, false
	}
	var mk oplog.Mark
	if b, err := os.ReadFile(mark); err == nil && json.Unmarshal(b, &mk) == nil && strings.HasPrefix(mk.Identity, m.logPrefix()) {
		return mk.Identity, true
	}
	return m.logPlaceholder(), true
}

func (m *pairMember) logPrefix() string      { return m.o.Cluster + "/" + string(m.o.Self) + "/" }
func (m *pairMember) logPlaceholder() string { return m.logPrefix() + "(box not read yet)" }

// rebindLog gives a provisionally opened log its identity once the box
// reads (logIdentity).
func (m *pairMember) rebindLog() {
	if !m.logPending.Load() {
		return
	}
	box := m.box()
	if box == "" {
		return
	}
	if err := m.log.Rebind(m.logPrefix()+box, m.logPlaceholder()); err != nil {
		slog.Error("redundancy: the operator-intent log can't take this box's identity", "error", err)
		return
	}
	m.logPending.Store(false)
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
	ls.History, ls.HistoryBy, ls.SupersedeTo, ls.Superseded = st.History, st.HistoryBy, st.SupersedeTo, st.Superseded
	for _, f := range st.Forked {
		ls.Forked = append(ls.Forked, f.Peer)
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

// The pair's Quorum answers the commissioning routes (a fake in the
// server's tests would not say when the interface moves).
var _ server.Commissioner = (*leader.Quorum)(nil)

// checkoutDuties are what a pair's controller does about the I/O checkout (#719
// slice 5) besides serving it, once a second until ctx ends (see checkoutDuty).
func (m *pairMember) checkoutDuties(ctx context.Context, st *checkout.Store, reconcile func() (int, error), demoted func()) {
	d := &checkoutDuty{stillLeads: m.q.IsLeader, halted: st.Halted, announce: st.AnnounceKey, name: string(m.o.Self), reconcile: reconcile, demoted: demoted, condition: m.q.SetCondition}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.step(m.q.IsLeader(), m.q.Term())
		}
	}
}

// checkoutDuty is one controller's checkout duties, a step at a time:
//
//   - a lead that is lost, or won again in another term between two steps, lets
//     the force this controller drove go at once (demoted: unforced here, nothing
//     proposed, since it no longer leads). A runtime's own takeover has dropped
//     its forces; the checkout's "I drive it" state must not outlive that. This
//     is tracked by term, not a flag sampled once a second, and runs whatever
//     state the checkout store is in;
//   - a checkout that stopped on an entry from a newer build is a standing
//     condition of the pair (naut redundancy status, /api/cluster), with its fix;
//     its state is stale, so it neither announces its key nor ends forces;
//   - it says which key it signs with, in the log, so both pages show both keys
//     (asked again until the log takes it);
//   - on the controller that leads, and only there, it ends a force the log has
//     standing that this controller is not driving (the other controller's, or a
//     start whose answer was lost, once its log has synced); on a standby it would
//     propose ends for a force the leader is driving.
type checkoutDuty struct {
	stillLeads func() bool // read again just before reconcile: announce can block for seconds
	halted     func() string
	announce   func(controller string) error
	name       string
	reconcile  func() (int, error)
	demoted    func()
	condition  func(key, msg string)

	led      bool
	term     uint64
	shown    string // the halt shown as a condition
	lastWarn string // the last reconcile error logged, so a standing one is said once
}

func (d *checkoutDuty) step(lead bool, term uint64) {
	// leadership first, in any state of the store
	if !lead || (d.led && term != d.term) {
		// every step off the lead, not only the first: a lead won, a force started and
		// the lead lost between two steps was never seen as led; demoted is a no-op
		// when no force stands here
		d.demoted()
		d.led = false
	}
	if lead && !d.led {
		d.led, d.term = true, term
	}
	if why := d.halted(); why != d.shown {
		d.shown = why
		if why == "" {
			d.condition("checkout-halted", "")
		} else {
			d.condition("checkout-halted", "the I/O checkout has stopped on this controller: "+why+". Update this controller to the build the other one runs, then restart it; the oplog, notes, alarms and substitutes are unaffected")
		}
	}
	if d.shown != "" {
		return // its checkout state is stale: it neither announces nor ends forces from it
	}
	if err := d.announce(d.name); err != nil {
		slog.Debug("checkout: the key is not announced yet", "error", err)
	}
	if !lead || (d.stillLeads != nil && !d.stillLeads()) {
		return // a standby proposing an end would end the force the leader is driving
	}
	switch n, err := d.reconcile(); {
	case err != nil:
		if err.Error() != d.lastWarn { // asked again each step; said when it changes
			d.lastWarn = err.Error()
			slog.Warn("checkout: could not end a force the log still has", "error", err)
		}
	case n > 0:
		d.lastWarn = ""
		slog.Warn("checkout: a force nothing on this controller drives was recorded as interrupted (the other controller's, or one whose start was not confirmed)", "forces", n)
	default:
		d.lastWarn = ""
	}
}
