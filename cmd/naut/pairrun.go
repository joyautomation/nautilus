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
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
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
	o   node.Options
}

// pairDeps are what newPairMember reads from the process: its
// environment and its listen call. Tests hand it their own.
type pairDeps struct {
	getenv func(string) string
	listen func(addr string) (net.Listener, error)
}

func osPairDeps() pairDeps {
	return pairDeps{getenv: os.Getenv, listen: func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }}
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
	if err := os.MkdirAll(filepath.Dir(o.StatePath), 0o750); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	// The election first: it locks the state file, so a second process of
	// this controller stops here, before it binds anything.
	q, err := leader.NewQuorum(o)
	if err != nil {
		return nil, err
	}
	h, err := node.Guard(o.Token, q.Node().Handler())
	if err != nil {
		q.Node().Close()
		return nil, err
	}
	ln, err := d.listen(listenAddr)
	if err != nil {
		q.Node().Close()
		return nil, fmt.Errorf("peer port %s: %w: is another controller (this one's service?) running?", listenAddr, err)
	}
	m := &pairMember{q: q, srv: peerServer(h), o: o}
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
		StatePath: filepath.Join(stateDir, pairStateFile), Token: token,
		ClientToken: getenv("NAUTILUS_TOKEN"),
		SelfAPI:     apiOn(r.Peers[self], apiAddr), PeerAPI: apiOn(r.Peers[peer], apiAddr),
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

// Close stops the peer listener and frees the state file. Call it after
// the election's Run has returned and Release has run: the peer listener
// answers votes until then.
func (m *pairMember) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
	m.q.Node().Close()
}

// peerServer serves a peer listener (a pair member's, the witness's): a
// vote is one small frame, so a slow or idle client is cut off.
func peerServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 8 << 10}
}
