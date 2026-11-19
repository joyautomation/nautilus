package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// What the hello exchanges besides the client-token check (design §2,
// test plan F30; #710 PR 3b-2b). Each side sends its own with every hello
// it asks or answers, and records the peer's:
//
//   - Config: a hash of the pair's members as this controller read them
//     (Options.Config: peers and witness). Members that differ between A
//     and B are a misconfiguration (two witnesses would make two
//     majorities): raised as EventConfigDiffers, and while the peer says
//     so (heard within cfgStale) a controller that does not lead does not
//     campaign. A leader keeps renewing, so a running plant is not stopped
//     by it, and a peer that has gone quiet holds nothing. The lease is
//     not in it: the voters already refuse a longer lease
//     (EventLeaseMismatch).
//   - Nonce: the boot nonce. A peer name answering under a nonce it had
//     already given up for a newer one (N1, N2, N1) is two processes
//     claiming that name: EventDuplicateMember.
//   - Caps: what this build does that the other must too. KeepsLiveIO
//     (#1013): it keeps a live I/O apply across a takeover.

// KeepsLiveIO is the capability a build advertises when it keeps a live
// I/O apply (retain io, #1010) across a takeover.
const KeepsLiveIO = "keeps-live-io"

// peerInfo is one side's part of the hello.
type peerInfo struct {
	Config string
	Nonce  uint64
	Caps   []string
}

func (e *Elector) selfInfo() peerInfo {
	return peerInfo{Config: e.o.Config, Nonce: e.nonce, Caps: e.o.Caps}
}

// query adds pi to a hello's query.
func (pi peerInfo) query(q url.Values) {
	q.Set("cfg", pi.Config)
	q.Set("n", strconv.FormatUint(pi.Nonce, 16))
	q.Set("caps", strings.Join(pi.Caps, ","))
}

// infoFromQuery reads the asker's part from a hello's query.
func infoFromQuery(q url.Values) (peerInfo, bool) {
	n, err := strconv.ParseUint(q.Get("n"), 16, 64)
	if err != nil || n == 0 {
		return peerInfo{}, false
	}
	pi := peerInfo{Config: q.Get("cfg"), Nonce: n}
	if c := q.Get("caps"); c != "" {
		pi.Caps = strings.Split(c, ",")
	}
	return pi, true
}

// infoMAC binds the answerer's part to the asker's challenge and both
// names, keyed with the peer token, as tokenMAC does for the client token.
func infoMAC(peerToken, challenge string, asker, answerer quorum.NodeID, pi peerInfo) string {
	m := hmac.New(sha256.New, []byte(peerToken))
	for _, part := range []string{"nautilus peer info answer", string(asker), string(answerer), challenge,
		pi.Config, strconv.FormatUint(pi.Nonce, 16), strings.Join(pi.Caps, ",")} {
		m.Write([]byte(part))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

// ConfigHash is what a controller passes as Options.Config: a short hash
// of the pair's members as it read them, which both controllers must
// agree on.
func ConfigHash(peerA, peerB, witness string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("a=%s\x00b=%s\x00w=%s", peerA, peerB, witness)))
	return hex.EncodeToString(sum[:8])
}

// cfgStale is how long a mismatch the peer reported keeps holding
// campaigns: three hello intervals (helloEvery). A peer that has gone
// quiet (down, or its link) holds nothing past it.
func (e *Elector) cfgStale() time.Duration { return 3 * helloEvery(e.o.Lease) }

// heard records the peer's part of a hello. Takes mu.
func (e *Elector) heard(pi peerInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.peerCaps, e.peerKnown = slices.Clone(pi.Caps), true

	differs := e.o.Config != "" && pi.Config != "" && pi.Config != e.o.Config
	if differs {
		e.cfgDiffAt = time.Now()
	}
	if differs != e.cfgDiffers {
		e.cfgDiffers = differs
		if differs {
			msg := fmt.Sprintf("the pair's members differ between %s and %s (redundancy: peers: or witness:): both must run the same section. Until they match, the controller that is not leading will not take over, so if the leader's lease lapses meanwhile control stops", e.o.Self, e.o.Peer)
			e.event(Event{Kind: EventConfigDiffers, Detail: msg})
			e.log.Error("redundancy: "+msg, "self", e.o.Config, "peer", pi.Config)
		} else {
			e.event(Event{Kind: EventConfigDiffers, Detail: "same"})
			e.log.Info("redundancy: the peer's configuration matches again")
		}
	}

	// Boot nonces the peer's name has answered under, oldest first.
	switch i := slices.Index(e.peerNonces, pi.Nonce); {
	case i >= 0 && i == len(e.peerNonces)-1:
	case i >= 0 && !mut("hello_duplicate_unseen"):
		// An older nonce again after a newer one: two processes.
		e.peerNonces = append(slices.Delete(e.peerNonces, i, i+1), pi.Nonce)
		e.helloDups++
		if t := time.Now(); t.Sub(e.helloDupAt) >= dupEvery {
			e.helloDupAt = t
			msg := duplicateMsg(e.o.Peer)
			e.event(Event{Kind: EventDuplicateMember, Detail: msg})
			e.log.Error("redundancy: " + msg + " (seen at the hello)")
		}
	default:
		e.peerNonces = append(e.peerNonces, pi.Nonce)
		if len(e.peerNonces) > 4 {
			e.peerNonces = e.peerNonces[1:]
		}
	}
}

// PeerCaps is what the peer last said its build does (Caps), and whether
// it has said anything this boot.
func (e *Elector) PeerCaps() ([]string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.peerCaps), e.peerKnown
}

// mayCampaign reports whether the candidate may campaign now: not while
// the peer says, at a recent hello, that the pair's members differ. A
// leader may always renew. Under mu.
func (e *Elector) mayCampaign(now quorum.Instant) bool {
	held := e.cfgDiffers && time.Since(e.cfgDiffAt) < e.cfgStale()
	return !held || e.c.Leading(now) || mut("config_differs_campaigns")
}
