package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum/node"
)

// peerTLSOff is the standing warning of a pair member or witness running
// with peer TLS off (#710): shown at every start, on /api/cluster and the
// banner.
const peerTLSOff = "peer TLS is off: votes, the operator-intent log and the peer token cross the network as plain HTTP, readable and replayable by anyone on the path. Run it like this only on a lab bench or a link nothing else reaches; drop the setting to turn it back on"

// peerKeyFile and peerPinsFile are a member's box key and pins (#710
// per-box keys), in its state directory. The witness keeps one pins file
// per cluster, beside the cluster's members file.
const (
	peerKeyFile  = "peer-key.json"
	peerPinsFile = "peer-pins.json"
)

// peerTLSIsOff reads a peer TLS setting ("" or "on" = on, "off"): with
// the peer_tls_default_off mutation, only an explicit "on" is on.
func peerTLSIsOff(setting string) bool {
	if mutNaut("peer_tls_default_off") {
		return setting != "on"
	}
	return setting == "off"
}

// peerTLSWarnings are naut check's findings about a pair's peer TLS
// (#710): off (the network then carries the token in the clear), or on
// with a peer token in this environment that naut run would refuse, so
// an upgrade is not where it shows first. Warnings: check runs on laptops
// that hold no token, and on boxes that do.
func peerTLSWarnings(proj *project.Project, getenv func(string) string) []string {
	r := proj.Redundancy
	if !r.Pair() {
		return nil
	}
	if peerTLSIsOff(r.PeerTLS) {
		return []string{"redundancy.peer-tls: off: " + peerTLSOff}
	}
	if tok := getenv("NAUTILUS_PEER_TOKEN"); tok != "" && len(tok) < peertls.MinToken {
		return []string{fmt.Sprintf("NAUTILUS_PEER_TOKEN here is %d characters: naut run refuses to start a pair member with it, since peer TLS (on by default) needs at least %d. Draw a new one with `naut redundancy new-pair` and give it to both controllers and the witness", len(tok), peertls.MinToken)}
	}
	return nil
}

// buildPeerTLS is a pair member's or the witness's peer TLS (#710): nil
// when off. offHint is how this program turns it off, for the refusal of
// a short token.
func buildPeerTLS(o peertls.Options, off bool, offHint string) (*peertls.Peer, error) {
	if off {
		return nil, nil
	}
	p, err := peertls.New(o)
	var short *peertls.ShortTokenError
	if errors.As(err, &short) {
		return nil, shortTokenErr(short.Len, offHint)
	}
	return p, err
}

func shortTokenErr(n int, offHint string) error {
	return fmt.Errorf("NAUTILUS_PEER_TOKEN is %d characters, and peer TLS (on by default) needs at least %d: its key is derived from the token, and anyone who reaches a peer port could test guesses at a short one offline. Draw a new one with `naut redundancy new-pair` and give it to both controllers and the witness (or, on a lab bench only, set %s)", n, peertls.MinToken, offHint)
}

// peerWrap is a member's peer TLS client side: each dialed address is the
// member named for it (a controller's peer by its peers: entry, the
// witness by its address), checked as that member.
func peerWrap(p *peertls.Peer, names map[string]string) node.TLSWrap {
	return func(ctx context.Context, raw net.Conn, addr string) (net.Conn, error) {
		name, ok := names[addr]
		if !ok {
			raw.Close()
			return nil, fmt.Errorf("peer TLS: %s is not a member's address", addr)
		}
		return p.Wrap(ctx, raw, name, addr)
	}
}

// peerKeys is a member's PeerKeys block (naut redundancy status,
// /api/cluster).
func peerKeys(p *peertls.Peer) *leader.PeerKeys {
	k := &leader.PeerKeys{Pinned: p.Pinned("")}
	fp, err := p.FingerprintNow() // a status call never waits behind a handshake's box read
	if err != nil {
		k.Error = err.Error()
	}
	k.Self = fp
	return k
}

// sysNet is where a box's network interfaces are read (hostKeyBox).
var sysNet = "/sys/class/net"

// readMachineID is this box's machine-id ("" when none).
func readMachineID() string {
	for _, f := range machineIDFiles {
		if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// hostKeyBox is the box a member's peer key is bound to (#710 per-box
// keys, peertls.Box): its machine-id and its physical NICs' MACs, or,
// with no physical NIC (a container), the MACs of the interfaces its
// links leave by. Unknown (no error) while a link doesn't resolve.
func hostKeyBox(links []string) (peertls.Box, error) {
	id := readMachineID()
	if id == "" {
		return peertls.Box{}, fmt.Errorf("no machine-id (%s): a pair member binds its peer key to its box, so a cloned disk never passes as the original; give this box a machine-id (systemd-machine-id-setup)", strings.Join(machineIDFiles, ", "))
	}
	nics, err := physicalNICs(sysNet)
	if err != nil {
		return peertls.Box{}, err
	}
	if len(nics) == 0 {
		for _, l := range links {
			mac, ok := linkMAC(l)
			if !ok {
				return peertls.Box{}, nil
			}
			if mac != "" {
				nics = append(nics, mac)
			}
		}
	}
	return peertls.Box{MachineID: id, NICs: nics}, nil
}

// witnessKeyBox is the witness's key box: hostKeyBox with no links (a
// witness with no physical NIC is bound by its machine-id alone).
var witnessKeyBox = hostKeyBox

// physicalNICs are the MACs of the physical network interfaces under
// root (/sys/class/net): those backed by a device. A bridge, a VLAN, a
// bond, a tun, a veth and docker's are not, so adding one, or moving a NIC
// into a bridge, never changes the list. A bond's enslaved NIC reports its
// own permanent address, not the bond's.
func physicalNICs(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("network interfaces (%s): %w", root, err)
	}
	var out []string
	for _, e := range ents {
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil && !mutNaut("key_box_virtual_nics") {
			continue
		}
		mac := readTrim(filepath.Join(dir, "bonding_slave", "perm_hwaddr"))
		if mac == "" {
			mac = readTrim(filepath.Join(dir, "address"))
		}
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, mac)
	}
	return out, nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// pinLog takes a member's peer TLS events (#710 per-box keys): every pin,
// refusal, drop, reset pins file and new key is logged and journalled
// (redundancy.peer-pin, with the name, the key's fingerprint, the peer's
// address and the time), and a refusal or a reset raises a condition. The
// first contacts come before the journal is attached (SetJournal), so
// events wait for it.
type pinLog struct {
	who string // "witness" or "redundancy"
	// slogOnly: no journal will be attached (the witness): nothing waits.
	slogOnly bool
	mu       sync.Mutex
	j        audit.Recorder
	// pending are events from before the journal, oldest first.
	pending []peertls.Event
	cond    func(key, msg string) // nil until attached
	conds   map[string]string
	timers  map[string]*time.Timer
	// lastRefused is each name's last refusal, for its condition's quiet
	// time (a stale timer never clears a fresh refusal).
	lastRefused map[string]time.Time
	// overflowed: the waiting events passed maxPendingPins (said once).
	overflowed bool
	// settled reports whether every name is pinned again (a reset
	// condition clears then); nil: never.
	settled func() bool
}

// maxPendingPins bounds the events kept for a journal not attached yet.
const maxPendingPins = 256

// refusedQuiet is how long a refusal's condition stands after the last
// refusal of that name.
var refusedQuiet = 2 * time.Minute

// resetStands is how long a reset pins file's condition stands: past the
// members' re-pinning, so an operator sees it and checks the fingerprints.
var resetStands = 24 * time.Hour

func newPinLog(who string) *pinLog {
	return &pinLog{who: who, conds: map[string]string{}, timers: map[string]*time.Timer{}, lastRefused: map[string]time.Time{}}
}

// event is peertls.Options.OnEvent.
func (l *pinLog) event(e peertls.Event) {
	attrs := []any{"kind", string(e.Kind), "name", e.Name, "fingerprint", e.Fingerprint}
	if e.Cluster != "" {
		attrs = append(attrs, "cluster", e.Cluster)
	}
	if e.Pinned != "" {
		attrs = append(attrs, "pinned", e.Pinned)
	}
	if e.Addr != "" {
		attrs = append(attrs, "addr", e.Addr)
	}
	msg := l.who + ": peer key " + string(e.Kind) + ": " + e.Detail
	switch e.Kind {
	case peertls.EventRefused, peertls.EventPinsReset, peertls.EventPinFailed:
		slog.Error(msg, attrs...)
	case peertls.EventNewKey, peertls.EventDropped:
		slog.Warn(msg, attrs...)
	default:
		slog.Info(msg, attrs...)
	}
	l.mu.Lock()
	if l.j == nil && !l.slogOnly && !mutNaut("pin_journal_unbuffered") {
		if len(l.pending) < maxPendingPins {
			l.pending = append(l.pending, e)
		} else if !l.overflowed {
			l.overflowed = true
			slog.Warn(l.who+": more peer key events than wait for the journal: the later ones are in this log only", "kept", maxPendingPins)
		}
	} else if l.j != nil {
		record(l.j, e)
	}
	switch e.Kind {
	case peertls.EventRefused:
		key := "peer-key-refused/" + e.Name
		l.setLocked(key, fmt.Sprintf("refused %s's peer TLS key at %s (from %s): %s", e.Name, e.Time.Format(time.RFC3339), nonEmpty(e.Addr, "an unknown address"), e.Detail))
		l.lastRefused[e.Name] = time.Now()
		if t := l.timers[key]; t != nil {
			t.Stop()
		}
		name := e.Name
		var clearIfQuiet func()
		clearIfQuiet = func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if left := refusedQuiet - time.Since(l.lastRefused[name]); left > 0 {
				l.timers[key] = time.AfterFunc(left, clearIfQuiet) // a newer refusal came
				return
			}
			l.setLocked(key, "")
		}
		l.timers[key] = time.AfterFunc(refusedQuiet, clearIfQuiet)
	case peertls.EventPinsReset:
		l.setLocked("peer-pins-reset", e.Detail)
		if t := l.timers["peer-pins-reset"]; t != nil {
			t.Stop()
		}
		l.timers["peer-pins-reset"] = time.AfterFunc(resetStands, func() { l.mu.Lock(); l.setLocked("peer-pins-reset", ""); l.mu.Unlock() })
	case peertls.EventPinFailed:
		key := "peer-pin-write/" + e.Name
		l.setLocked(key, e.Detail)
		if t := l.timers[key]; t != nil {
			t.Stop()
		}
		// A member that stops trying never pins: its condition goes after
		// a quiet day (each retry raises it again).
		l.timers[key] = time.AfterFunc(resetStands, func() { l.mu.Lock(); l.setLocked(key, ""); l.mu.Unlock() })
	case peertls.EventPinned:
		if m, on := l.conds["peer-pins-reset"]; on && l.settled != nil && l.settled() && !strings.Contains(m, "pinned again") {
			// It stands (resetStands): the members are pinned again, and
			// an operator checks that they are the right keys.
			l.setLocked("peer-pins-reset", m+". Every member is pinned again since "+e.Time.Format(time.RFC3339)+": check each fingerprint (naut redundancy status) against the box itself")
		}
		l.setLocked("peer-key-refused/"+e.Name, "")
		l.setLocked("peer-pin-write/"+e.Name, "")
	}
	l.mu.Unlock()
}

func (l *pinLog) setLocked(key, msg string) {
	if msg == "" {
		delete(l.conds, key)
	} else {
		l.conds[key] = msg
	}
	if l.cond != nil {
		l.cond(key, msg)
	}
}

// attachConditions raises the conditions through set (Quorum.SetCondition),
// those already standing first.
func (l *pinLog) attachConditions(set func(key, msg string), settled func() bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cond, l.settled = set, settled
	for k, m := range l.conds {
		set(k, m)
	}
}

// attachJournal records the waiting events, then every later one.
func (l *pinLog) attachJournal(j audit.Recorder) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.j = j
	for _, e := range l.pending {
		record(j, e)
	}
	l.pending = nil
}

func record(j audit.Recorder, e peertls.Event) {
	res := audit.ResultOK
	if e.Kind == peertls.EventRefused {
		res = audit.ResultRefused
	}
	d := map[string]any{"kind": string(e.Kind), "fingerprint": e.Fingerprint, "detail": e.Detail, "time": e.Time.UTC().Format(time.RFC3339Nano)}
	for k, v := range map[string]string{"cluster": e.Cluster, "pinned": e.Pinned, "addr": e.Addr} {
		if v != "" {
			d[k] = v
		}
	}
	_, _ = j.Record(audit.Entry{User: "system", Source: audit.SourceSystem, Action: "redundancy.peer-pin", Target: e.Name, Detail: d, Result: res})
}
