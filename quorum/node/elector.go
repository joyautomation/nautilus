package node

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Defaults (design §2).
const (
	// DefaultLease is a pair's and its witness's lease when none is
	// given: 1 s (#710; it was 2 s). Keep it equal to project's
	// DefaultPairLease (cmd/naut tests it).
	DefaultLease = time.Second
	DefaultRho   = 0.01
	// WitnessID is the witness's name in the vote protocol: one witness
	// per pair, so it needs no other.
	WitnessID quorum.NodeID = "w"
)

// bootClock is quorum.BootClock; a test flips it to check the refusal.
var bootClock = quorum.BootClock

// Options configures a controller's Elector.
type Options struct {
	// Cluster is the pair's cluster ID (NewClusterID): drawn once, when
	// the pair is commissioned, and given to both controllers and the
	// witness as NAUTILUS_CLUSTER, beside the peer token and outside the
	// state directory, so a wiped state directory still re-adopts from the
	// peer (#976). The witness keeps one voter per cluster, bound to the
	// pair's two names, so pairs never share one (not even two sites that
	// run one project).
	Cluster string
	// Self and Peer name the two controllers (NAUTILUS_NODE: a or b);
	// Witness defaults to WitnessID.
	Self, Peer, Witness quorum.NodeID
	// PeerAddr and WitnessAddr are where their peer listeners are:
	// host:port, or a base URL.
	PeerAddr, WitnessAddr string
	// Lease defaults to DefaultLease, Rho to DefaultRho.
	Lease time.Duration
	Rho   float64
	// NetDelay (d_net) is the plant network's assumed one-way delivery
	// bound and SendMargin (m_send) the gap a send path may leave between
	// its gate check and its send system call (design §3.4). The write
	// gate closes NetDelay+SendMargin before the lease ends (the fence
	// margin), and a send's budget is the lease left minus NetDelay. Zero
	// takes FenceDefaults(Lease).
	NetDelay, SendMargin time.Duration
	// StatePath is this controller's voter state file. It belongs in the
	// controller's state directory, never in the project tree: a deploy
	// replaces the project, and a lost file needs the peer, or an
	// operator, to re-commission.
	StatePath string
	// Token is the peer token (NAUTILUS_PEER_TOKEN), required.
	Token string
	// ClientToken is the tag API's token (NAUTILUS_TOKEN). It never leaves
	// this process: only an HMAC of it, for the peer to compare (design
	// §6.1).
	ClientToken string
	// SelfAPI is this controller's tag API address (host:port) as the peer
	// reaches it. Status.LeaderAddr names it while this controller leads,
	// and the hello announces it to the peer, whose Status.LeaderAddr
	// names it while this one leads: for a standby's proxy and for clients
	// that follow the leader. The peer's is never guessed from this one's
	// port (#710 demo finding 1).
	SelfAPI string
	// Config is a hash of the pair's section as this controller read it
	// (ConfigHash), and Caps what its build does that the peer must too
	// (KeepsLiveIO): both sent at every hello (peerinfo.go). Config ""
	// skips the comparison.
	Config string
	Caps   []string
	// Credentials, when set, is read at every hello and compared with the
	// peer's (#708, report only): EventUsersDiffer, EventTLSDiffers.
	// Nil: neither part.
	Credentials func() Credentials
	// Box is the fingerprint of the box this controller runs on
	// (BoxFingerprint), stamped in the state file. A file written on
	// another box is treated as lost (box.go, #1029). "": no check.
	Box string
	// BoxFunc, when set, reads the fingerprint instead of Box, and is read
	// again until it answers: while it answers "" (a peer address that
	// doesn't resolve yet), the controller neither votes nor campaigns
	// (Run waits), and leaves its file untouched. It never skips the
	// check (#1034 review).
	BoxFunc func() string
	// Dial, when set, opens the peer connections (default: net.Dialer).
	// Tests cut links with it.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS, when set, is the pair's peer TLS client configuration
	// (internal/peertls, #710): every peer call is then https. Nil: plain
	// HTTP (redundancy.peer-tls: off, and tests).
	TLS *tls.Config
	// Log defaults to slog.Default.
	Log *slog.Logger
	// OnEvent, when set, gets every Event, in order, from one goroutine.
	OnEvent func(Event)

	// For tests: the clock (quorum.Now), the HTTP client, and the state
	// file's write.
	clock  func() quorum.Instant
	client *http.Client
	write  func(*os.File, []byte) (int, error)
}

// EventKind names an Event. The names follow design §7.1's audit actions.
type EventKind string

const (
	// EventAdopted: this controller durably adopted (Gen, Incarnation).
	EventAdopted EventKind = "redundancy.witness.incarnation"
	// EventTwoCopies: two witness incarnations answered within a lease:
	// two copies of the witness are running.
	EventTwoCopies EventKind = "redundancy.witness.copies"
	// EventRecommissionNeeded: this controller can't re-commission on its
	// own; an operator must, with a term floor (quorum.EventRecommission).
	EventRecommissionNeeded EventKind = "redundancy.witness.lost"
	// EventReadoptOverSwitch: quorum.EventReadoptOverSwitch.
	EventReadoptOverSwitch EventKind = "redundancy.witness.readopt-over-switch"
	// EventRecommissioned: an operator's Recommission was made durable.
	EventRecommissioned EventKind = "redundancy.recommission"
	// EventClientToken: the client-token check changed its answer
	// (Detail: same or differs).
	EventClientToken EventKind = "redundancy.client-token"
	// EventStateWrite: the state file could not be written (Detail: the
	// error); this voter grants nothing until a write succeeds. Raised
	// once per failing run, and again with Detail "ok" when it recovers.
	EventStateWrite EventKind = "redundancy.state-write"
	// EventLeaseMismatch: a voter refused a vote because its stated lease
	// is longer than the voter's (Detail says which voter and both
	// leases). The pair's members (or the witness's --lease) disagree.
	// Raised on each side once per change: Event.Voter names the other
	// side and Event.Inbound says whose request was refused. Detail ""
	// clears it, once that voter next takes this controller's lease (a
	// reply of any kind but a lease refusal), or this controller's voter
	// next takes the peer's (#710).
	EventLeaseMismatch EventKind = "redundancy.lease-mismatch"
	// EventTermJumpRefused: a voter refused a vote asking for a term more
	// than MaxTermJump above what it promised (Detail: which voter, the
	// terms). Raised on each side once per change.
	EventTermJumpRefused EventKind = "redundancy.term-jump-refused"
	// EventVersionMismatch: a vote or handshake frame was refused for its
	// protocol version, either way (Detail: who and which versions): the
	// pair and the witness run releases that can't vote with each other.
	// Raised on each side once per change.
	EventVersionMismatch EventKind = "redundancy.version-mismatch"
	// EventDuplicateMember: this controller's voter refused a vote from a
	// name whose running promise was granted under another boot nonce:
	// two processes claim one member of the pair (an env file copied to
	// the wrong box, a cloned disk; design §2). Detail names it. Raised at
	// most once every dupEvery while it goes on.
	EventDuplicateMember EventKind = "redundancy.duplicate-member"
	// EventDuplicateMarked: a voter marks THIS controller's name a
	// confirmed duplicate (quorum.Reply.Marked): another process runs as
	// it, and this controller campaigns no more until the mark is cleared
	// (Detail: which voter). "" Detail once no voter marks it.
	EventDuplicateMarked EventKind = "redundancy.duplicate-marked"
	// EventDuplicateCleared: an operator cleared this controller's voter's
	// mark on a name (ClearDuplicate). Detail names it.
	EventDuplicateCleared EventKind = "redundancy.duplicate-cleared"
	// EventConfigDiffers: the peer's hello names another configuration of
	// the pair (Detail says so), or "same" once it matches again. While
	// it differs, a controller that does not lead does not campaign.
	EventConfigDiffers EventKind = "redundancy.config-differs"
	// EventUsersDiffer and EventTLSDiffers: the credential check (#708)
	// found the pair's users files, or its tag API certificates, not the
	// same, or one unreadable (Detail is the message), or "" once they
	// match again (or neither has any). Report only.
	EventUsersDiffer EventKind = "redundancy.credentials.users"
	EventTLSDiffers  EventKind = "redundancy.credentials.tls"
	// EventContactsDiffer: the alarm contacts check (#715): the two
	// controllers' alarm-contacts.json and alarm-secrets.json differ, or
	// one cannot read its own. Report only, as the credential check is:
	// never the vote, the lease or the write gate.
	EventContactsDiffer EventKind = "redundancy.credentials.contacts"
	// EventBoxChanged: the voter state was written on another box (#1029):
	// treated as lost, so this controller starts uncommissioned. Detail
	// says what changed and how to tell a NIC swap from a clone.
	EventBoxChanged EventKind = "redundancy.box-changed"
	// EventSpare: this controller is a COPY of a member that both other
	// voters hold another boot of the name for (#1029 3c-2): it demoted
	// itself to a spare, wrote that down, and is silent: no vote, no
	// campaign, no hello, no answer to the peer. Raised at the decision
	// and at every start of a spare (Detail says what to do).
	EventSpare EventKind = "redundancy.spare"
	// EventSuperseded: a voter refused this controller as fenced: its
	// state is from before an operator replaced it with recommission
	// --from-spare (the box that was switched off and on again, its old
	// disk in it), and it can't vote any more (#1029 3c-3c-1b). It
	// campaigns no more in this boot. Once per boot.
	EventSuperseded EventKind = "redundancy.superseded"
	// EventPeerTLS: a voter speaks the other protocol on its peer port
	// (TLS where this controller speaks plain HTTP, or the reverse): peer
	// TLS is off on one side, or it runs a release from before peer TLS
	// (#710). Event.Voter names it; Detail says which and what to do, ""
	// once a call to it gets through again. Raised by the side that dials.
	EventPeerTLS EventKind = "redundancy.peer-tls-mismatch"
	// EventPeerIdentity: a voter's TLS key is not the pair's, or it
	// refused this controller's: another NAUTILUS_PEER_TOKEN there, or
	// something else answers at its address (#710). Event.Voter names
	// it; Detail "" once a call to it gets through again.
	EventPeerIdentity EventKind = "redundancy.peer-identity"
)

// supersededMsg is the old box's alarm (EventSuperseded): self's state is
// from before an operator replaced it; voter said so.
func supersededMsg(self, voter quorum.NodeID) string {
	return fmt.Sprintf("this box's state is from before %s was replaced (recommission --from-spare): it can no longer vote (%s refuses it). Wipe its state only with the network whole (move its state file aside: it then starts as a copy, is heard beside the replacement and made a spare), or keep it off", self, voter)
}

// versionMsg is the version-mismatch report for a frame of version got
// from (or answered by) who.
func versionMsg(who string, got byte) string {
	return fmt.Sprintf("%s speaks vote protocol version %d and this one %d: the pair and the witness run releases that can't vote with each other. Run one release on both controllers and the witness", who, got, quorum.WireVersion)
}

// olderPeerMsg: who refused this controller's vote frame as malformed,
// which is how a release before vote protocol version 2 answers one.
func olderPeerMsg(who string) string {
	return fmt.Sprintf("%s refused this controller's vote frame as malformed: it most likely runs a release older than vote protocol version %d, which can't vote with this one. Run one release on both controllers and the witness", who, quorum.WireVersion)
}

// boxChangedMsg is the box-changed report.
func boxChangedMsg(changed []string) string {
	return "the voter state was written on another box (" + strings.Join(changed, " and ") + " changed), so this controller starts " +
		"uncommissioned and does not vote. If this box's network card was replaced or the box was migrated, nothing is needed: " +
		"it re-adopts from the other controller within seconds (redundancy.witness.incarnation). If it stays uncommissioned while " +
		"the other controller is up, this box is a COPY of a controller (a cloned disk or VM): stop it, since a clone must never " +
		"run as a member, and never re-commission it"
}

// spareMsg is the spare report for this controller's name.
func spareMsg(self quorum.NodeID) string {
	return fmt.Sprintf("duplicate of %s - spare: another process runs as %s, and both other members hold that one (they heard it first) as %s. "+
		"This process is a COPY (a cloned disk or VM, or NAUTILUS_NODE set to %s on a second box) and has made itself a spare: it neither votes nor campaigns, and answers nobody, until an operator acts. "+
		"If this box is the copy, stop it (decommission). If this box should be %s (the real box, or a replacement for a dead original): stop the other process, confirm it is down (powered off or its process ended, not just unplugged or paused) and keep it from starting again, then on this box run naut redundancy recommission --from-spare --copy-stopped and restart naut: it re-adopts from the other controller", self, self, self, self, self)
}

// ErrSpare: a spare refuses an operator's re-commission (#1029 3c-2).
var ErrSpare = errors.New("quorum: this controller is a spare (a copy of a member): it takes no re-commission")

// newerStateMsg is the refusal of a state file in a format above this
// release's.
func newerStateMsg(path string) string {
	return fmt.Sprintf("the voter state file %s was written by a newer release of naut (format 6 and later: from 6 on, the box may have been made a spare, a copy of a member that must stay silent; from 7 on, it may hold duplicate marks; from 8 on, a fence on a member an operator replaced). "+
		"This release can't read it and won't start over it: run the release that wrote it. To run this release anyway, first make sure no other box runs as this member, then move the file aside: it starts uncommissioned and re-adopts from the other controller", path)
}

// dupEvery paces the duplicate-member report while it goes on.
const dupEvery = 10 * time.Second

// confirmedAlarm is the duplicate-member alarm for a mark the voter just
// made (quorum.Voter.MarkCount), or "" when it made none since markSeen.
// It follows the mark count, not DuplicateCause: a relay rule can count
// again in the very Handle that marked, and overwrite the cause. And it
// is never paced: it comes once per mark, and is the only word on how to
// clear it (#1113 review).
func confirmedAlarm(v *quorum.Voter, markSeen *uint64) string {
	m, name := v.MarkCount()
	if m == *markSeen || mut("mark_alarm_from_cause") {
		return ""
	}
	*markSeen = m
	return duplicateMsg(name, quorum.DupConfirmed)
}

// duplicateMsg is the duplicate-member report.
func duplicateMsg(who quorum.NodeID, cause quorum.DupCause) string {
	if cause == quorum.DupConfirmed {
		return fmt.Sprintf("two processes claim to be %s: this voter heard both running at once, and marks %s: it gives %s no new vote and counts no vote from %s until the mark is cleared. Clear it: (1) stop the copy and confirm it is down: powered off or its process ended, not just unplugged or paused (check NAUTILUS_NODE on every box, and that no box runs a clone of another's disk); (2) on either controller, run naut redundancy clear-duplicate %s --copy-stopped: it checks every member, clears the mark at each, and frees %s without a restart. A restart doesn't clear it: the mark is kept on disk. While a mark stands, a split or a lost witness can leave the pair without a leader", who, who, who, who, who, who)
	}
	if cause == quorum.DupVoterPromised {
		return fmt.Sprintf("two processes claim to be %s: %s campaigned while its own voter was heard promised to another controller, which %s never does. A copy of %s is running: check NAUTILUS_NODE on every box (it differs per box) and that no box runs a clone of another's disk", who, who, who, who)
	}
	return fmt.Sprintf("two processes claim to be %s: this voter hears %s under two boot nonces at once (a vote, a probe, or a promise another voter relayed), so it gives %s no new vote. Check NAUTILUS_NODE on every box (it differs per box) and that no box runs a clone of another's disk", who, who, who)
}

// Event is something the operator should see.
type Event struct {
	Kind             EventKind
	Gen, Incarnation uint64
	Other            uint64 // EventTwoCopies, EventReadoptOverSwitch
	Detail           string
	// Voter names the voter an EventPeerTLS, EventPeerIdentity,
	// EventLeaseMismatch, EventTermJumpRefused or EventVersionMismatch is
	// about.
	Voter quorum.NodeID
	// Inbound: a refusal event (lease, term jump, version) is about a
	// request of Voter's that this controller's voter refused; false:
	// Voter refused this controller's. The two are kept apart, so a pair
	// whose leases differ reports each side once and never flaps.
	Inbound bool
}

// Status is a snapshot of the elector, for the operator surfaces.
type Status struct {
	Self, Peer string
	// Leading reports the gate (IsLeader); Term is the last fencing token
	// this controller led under (Elector.Term).
	Leading bool
	Term    uint64
	// Leader is who leads as this controller sees it: itself, the peer
	// (seen holding a lease), or "". LeaderAddr is its tag API address
	// (Options.SelfAPI, or the address the peer announced in its last
	// verified hello), "" when none leads or it is not known.
	Leader     string
	LeaderAddr string
	// Commissioned: an incarnation adopted. A controller that isn't grants
	// nothing (a new pair, or a lost file).
	Commissioned           bool
	Gen, Incarnation       uint64
	PendingGen, PendingInc uint64
	Floor, KnownFloor      uint64
	// WitnessIncarnation is the last incarnation the witness answered
	// with, WitnessAge how long ago (-1: never in this boot).
	WitnessIncarnation uint64
	WitnessAge         time.Duration
	// PeerAge is how long ago the peer was last heard (-1: never).
	PeerAge time.Duration
	// RecommissionNeeded: EventRecommissionNeeded was raised and this
	// controller is still uncommissioned.
	RecommissionNeeded bool
	ClientToken        ClientToken
	// Users and TLS are what the credential check (#708) last found.
	Users, TLS CredState
	// Contacts is the alarm contacts check (#715), report only.
	Contacts CredState
	// StateWriteError is the last state write's error, "" while writes
	// succeed.
	StateWriteError string
	// Acquisitions counts the leaderships this boot has won.
	Acquisitions int
	// Duplicates counts votes this controller's voter refused because two
	// processes claim one member (EventDuplicateMember).
	Duplicates uint64
	// BoxChanged is the box-changed report when this start found its state
	// written on another box (EventBoxChanged), else "".
	BoxChanged string
	// BoxPending: this box's fingerprint can't be read yet, so this
	// controller neither votes nor campaigns (Options.BoxFunc).
	BoxPending bool
	// Spare: this controller is a copy of a member and silent (EventSpare,
	// #1029 3c-2). It names no leader: its tag API forwards nothing.
	Spare bool
	// FromSpare is where recommission --from-spare moved this spare's state
	// file (#1029 3c-3c): naut must be restarted on this box, "" otherwise.
	FromSpare string
	// Marked are the names this controller's voter marks as run by two
	// processes at once (#1029 3c), sorted. Held: a voter's last reply
	// marked this controller's own name, so it campaigns no more until the
	// mark is cleared (quorum.Candidate.Marked).
	Marked []string
	Held   bool
	// Superseded names a voter that refused this controller as fenced:
	// its state is from before an operator replaced it (EventSuperseded,
	// #1029 3c-3c-1b), "" otherwise.
	Superseded string
	// ConfigDiffers: the peer's hello named another configuration of the
	// pair (EventConfigDiffers). HelloDuplicates counts hellos that showed
	// two processes behind the peer's name.
	ConfigDiffers   bool
	HelloDuplicates uint64
}

// Elector is a controller's side of the vote: its voter, its candidate and
// its side of the witness-incarnation handshake, driven over the peer
// listener (Handler, behind Guard) and the state file.
type Elector struct {
	o     Options
	log   *slog.Logger
	now   func() quorum.Instant
	file  *State
	free  func()
	call  caller
	peer  string // base URLs
	wit   string
	nonce uint64

	mu        sync.Mutex
	v         *quorum.Voter
	c         *quorum.Candidate
	a         *quorum.Adoption
	health    func() bool
	released  bool
	unhealthy bool
	waiters   map[waitKey]chan quorum.Reply
	epoch     uint64 // the last epoch counted in acquisitions
	acquired  int
	witInc    uint64
	witAt     quorum.Instant
	witHeard  bool
	peerAt    quorum.Instant
	peerHeard bool
	needOp    bool
	dupSeen   uint64 // the voter's Duplicates count last reported
	// supersededTold: EventSuperseded was raised in this boot.
	supersededTold bool
	markSeen       uint64    // the voter's MarkCount last reported
	dupAt          time.Time // when it was last reported

	// The peer's part of the hello (peerinfo.go).
	peerCaps   []string
	peerKnown  bool
	peerAPI    string // its tag API, from a verified answer (heardAPI)
	cfgDiffers bool
	cfgDiffAt  time.Time // the last hello that showed it
	boxChanged string    // EventBoxChanged's report
	boxPending bool      // the box can't be read yet: Run waits (BoxFunc)
	fromSpare  string    // recommission --from-spare moved the state file here: restart naut
	// lookedAt is an uncommissioned controller's last look (pace); paced:
	// it has one.
	lookedAt    quorum.Instant
	paced       bool
	storedBox   string   // the box the state file named
	peerNonces  []uint64 // the boot nonces the peer's name answered under, oldest first
	helloDups   uint64
	helloDupAt  time.Time
	token       ClientToken
	users, tls  CredState // the credential check (#708)
	usersMsg    string    // the users condition last raised (it names who can't read)
	tlsMsg      string    // the TLS condition last raised
	contacts    CredState // the alarm contacts check (#715)
	contactsMsg string    // the contacts condition last raised
	writeErr    string
	mismatch    map[string]string // the last lease-mismatch report per voter, to report each change once
	links       map[string]string // the standing peer-TLS and identity reports, by kind and voter (noteLink)
	durable     uint64            // the highest Seq known on disk
	written     chan struct{}     // closed and replaced at every write report
	// spareSeq: the write of the spare flag in flight (Voter.MakeSpare),
	// 0 when none. spare: the flag is durable (or was read at start), and
	// this controller is silent (#1029 3c-2).
	spareSeq uint64
	spare    atomic.Bool
	// markFn: the standby's in-sync mark, asked at each look
	// (SetSyncMark).
	markFn func(epoch uint64) (uint64, bool)

	// The operator-intent log's seams and what they decided (logpref.go).
	logFn     func() LogState
	vouchFn   func(ctx context.Context, by string, inc uint64, acc LogAccept) (json.RawMessage, error)
	peerLog   peerLog
	holdSince time.Time      // since when the log has held a campaign
	yieldedAt time.Time      // the last yield (at most one per yieldEvery)
	yieldHold bool           // no renewal until the lease is seen over (a yield)
	yieldDue  quorum.Instant // when it was due to end (the yield_until_due mutation)
	// handBackUntil: a hand-back's hold (handback.go), no renewal and no
	// campaign before it, the gate closed.
	handBackUntil quorum.Instant
	// peerLeaseSeen: this boot's voter has seen the peer hold a lease;
	// tookOver: the last leadership acquired is a takeover (IsTakeover).
	peerLeaseSeen bool
	tookOver      atomic.Bool

	gate    atomic.Pointer[gateState]
	counted atomic.Uint64 // the witness incarnation the candidate counts (CountedIncarnation)
	wake    chan struct{}
	// helloNow asks the hello loop for a hello at once, from the fast
	// start: the link to the peer came up, or the peer's votes carry a new
	// boot nonce (it restarted). peerDown and peerVoteNonce, under mu, are
	// what those are judged against.
	helloNow      chan struct{}
	peerDown      bool
	peerVoteNonce uint64

	w       *writer
	running atomic.Bool
	ctx     context.Context // Run's: sends end with it
	events  chan Event
	decided chan struct{}
	once    sync.Once
}

// gateState is what the write gate is judged on, published after every
// call into the protocol and read without the lock (IsLeader). The lease
// end is judged when the gate is READ, not when it was published.
type gateState struct {
	epoch uint64         // the last fencing token led under (only rises)
	end   quorum.Instant // the lease end, on this node's clock (0: none)
	open  bool           // healthy, not released, and leading when published
}

// New starts a controller's elector from its state file: its voter waits
// one lease before it grants anything, and its candidate draws a fresh
// boot nonce. A missing or unreadable state file starts it uncommissioned:
// it grants nothing and re-adopts its peer's incarnation (or waits for an
// operator, Recommission). Controllers run on CLOCK_BOOTTIME only, so New
// refuses any other platform.
func New(o Options) (*Elector, error) {
	if o.Witness == "" {
		o.Witness = WitnessID
	}
	if o.Lease == 0 {
		o.Lease = DefaultLease
	}
	if o.Rho == 0 {
		o.Rho = DefaultRho
	}
	if dn, ms := FenceDefaults(o.Lease); o.NetDelay == 0 || o.SendMargin == 0 {
		if o.NetDelay == 0 {
			o.NetDelay = dn
		}
		if o.SendMargin == 0 {
			o.SendMargin = ms
		}
	}
	if f := o.NetDelay + o.SendMargin; o.NetDelay < 0 || o.SendMargin < 0 || f >= o.Lease/4 {
		return nil, fmt.Errorf("quorum: fence margin %s (net delay %s + send margin %s) must be under a quarter of the lease (%s)", f, o.NetDelay, o.SendMargin, o.Lease)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	switch {
	case o.clock == nil && !bootClock:
		return nil, errors.New("quorum: a redundant controller needs CLOCK_BOOTTIME (Linux): a lease must keep counting through a host suspend")
	case !ValidCluster(o.Cluster):
		return nil, fmt.Errorf("quorum: cluster ID %q: want 32 lowercase hex digits, drawn at commissioning", o.Cluster)
	case o.Token == "":
		return nil, errors.New("quorum: a pair member needs NAUTILUS_PEER_TOKEN")
	case o.PeerAddr == "" || o.WitnessAddr == "":
		return nil, errors.New("quorum: the peer's and the witness's addresses are both needed")
	case o.StatePath == "":
		return nil, errors.New("quorum: a state file path is needed")
	}
	now := o.clock
	if now == nil {
		now = quorum.Now
	}
	client := o.client
	if client == nil {
		client = newClient(o.Dial, o.TLS)
	}
	e := &Elector{
		o: o, log: o.Log.With("cluster", o.Cluster, "node", string(o.Self)), now: now,
		file: &State{Path: o.StatePath, Owner: o.Self, Cluster: o.Cluster, Box: o.Box, write: o.write},
		call: caller{client: client, token: o.Token, lease: o.Lease, members: members(o.Self, o.Peer)},
		peer: baseURL(o.PeerAddr, o.TLS != nil), wit: baseURL(o.WitnessAddr, o.TLS != nil), nonce: drawNonce(),
		waiters: map[waitKey]chan quorum.Reply{}, written: make(chan struct{}), mismatch: map[string]string{}, links: map[string]string{},
		wake: make(chan struct{}, 1), helloNow: make(chan struct{}, 1), events: make(chan Event, 64), decided: make(chan struct{}),
	}
	e.call.note, e.call.tls = e.noteLink, o.TLS != nil
	free := func() {}
	if !mut("controller_unlocked") {
		f, err := lock(o.StatePath + ".lock")
		if err != nil {
			return nil, err
		}
		free = f
	}
	box := o.Box
	if o.BoxFunc != nil {
		box = o.BoxFunc()
	}
	e.file.Box = box
	st, err := e.file.Load()
	if errors.Is(err, quorum.ErrVersion) && !mut("newer_state_moved_aside") {
		// Written by a newer release, a spare's maybe: never treated as
		// lost, which would make a copy a member again (#1029 3c-2).
		free()
		return nil, errors.New("redundancy: " + newerStateMsg(o.StatePath))
	}
	if err == nil {
		if err := e.file.CheckOwner(st); err != nil {
			free()
			return nil, err
		}
		if st.Spare && !mut("spare_forgotten_on_box_change") {
			// A spare stays one, read before the box check: a copy's box
			// differs, and the check would treat the file as lost.
			e.spare.Store(true)
			e.storedBox = st.Box
			e.event(Event{Kind: EventSpare, Detail: spareMsg(o.Self)})
			e.log.Error("redundancy: " + spareMsg(o.Self))
			t := now()
			if err := e.initProtocol(st, t); err != nil {
				free()
				return nil, err
			}
			e.free = free
			e.w = newWriter(e.file.Save, e.persisted)
			e.publish(t)
			return e, nil
		}
		e.storedBox = st.Box
		if box == "" && o.BoxFunc != nil && !mut("box_wait_skipped") {
			// This box can't be read yet: no vote and no campaign until it
			// can (Run), and the file untouched. Never a check skipped on
			// a guess (#1034 review).
			e.boxPending = true
			e.log.Warn("redundancy: this box's fingerprint can't be read yet (a peer or witness address doesn't resolve): this controller neither votes nor campaigns until it can", "file", o.StatePath)
		} else if box == "" {
			e.file.Box = st.Box // box_wait_skipped: the stored one carries on
		}
		if changed := boxChanges(st.Box, box); changed != nil && !e.boxPending {
			st = e.lostToAnotherBox(changed)
		}
	} else if box == "" && o.BoxFunc != nil && !mut("box_wait_skipped") {
		e.boxPending = true // nothing to protect, but nothing written without the box either
	}
	switch {
	case errors.Is(err, ErrNoState):
		e.log.Info("redundancy: no voter state: this controller is uncommissioned (a new pair, or a wiped disk) and grants nothing until it adopts the pair's witness incarnation", "file", o.StatePath)
	case err != nil:
		aside := fmt.Sprintf("%s.unreadable-%d", o.StatePath, time.Now().Unix())
		_ = os.Rename(o.StatePath, aside)
		e.log.Error("redundancy: the voter state can't be read: this controller is uncommissioned and grants nothing until it re-adopts the pair's witness incarnation, from its peer or an operator", "file", o.StatePath, "kept", aside, "err", err)
	}
	t := now()
	if err := e.initProtocol(st, t); err != nil {
		free()
		return nil, err
	}
	e.free = free
	e.w = newWriter(e.file.Save, e.persisted)
	e.publish(t)
	return e, nil
}

// initProtocol builds the voter, candidate and adoption from st.
func (e *Elector) initProtocol(st quorum.VoterState, t quorum.Instant) error {
	cfg := quorum.Config{Self: e.o.Self, Peer: e.o.Peer, Witness: e.o.Witness, Lease: e.o.Lease, Rho: e.o.Rho,
		Incarnation: st.Incarnation, Nonce: e.nonce, Jitter: jitter(e.o.Lease)}
	e.v = quorum.NewVoter(e.o.Self, e.o.Lease, st, t)
	c, err := quorum.NewCandidate(cfg, st.Promised, t)
	if err != nil {
		return err
	}
	e.c = c
	e.a = quorum.NewAdoption(e.v, c, t)
	e.v.SetBoot(e.nonce) // #1113 review: a hold ends only on this boot's word
	return nil
}

// lostToAnotherBox treats the state file as lost: written on another box,
// a cloned disk, or this box's NIC or host changed. Uncommissioned until
// it re-adopts from its peer (#976), which a clone never does (#1029).
func (e *Elector) lostToAnotherBox(changed []string) quorum.VoterState {
	aside := fmt.Sprintf("%s.other-box-%d", e.o.StatePath, time.Now().Unix())
	_ = os.Rename(e.o.StatePath, aside)
	msg := boxChangedMsg(changed)
	e.boxChanged = msg
	e.event(Event{Kind: EventBoxChanged, Detail: msg})
	e.log.Error("redundancy: "+msg, "file", e.o.StatePath, "kept", aside)
	return quorum.VoterState{}
}

// waitBox holds Run until the box can be read (Options.BoxFunc), then
// checks the stored state against it. False when ctx ends first.
func (e *Elector) waitBox(ctx context.Context) bool {
	every := max(e.o.Lease/4, 50*time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
		box := e.o.BoxFunc()
		if box == "" {
			continue
		}
		e.mu.Lock()
		e.boxPending = false
		e.file.Box = box
		if changed := boxChanges(e.storedBox, box); changed != nil {
			st := e.lostToAnotherBox(changed)
			if err := e.initProtocol(st, e.now()); err != nil {
				e.mu.Unlock()
				e.log.Error("redundancy: can't restart the vote protocol", "err", err)
				return false
			}
			e.publish(e.now())
		}
		e.mu.Unlock()
		e.log.Info("redundancy: this box's fingerprint is readable: this controller votes and campaigns")
		return true
	}
}

// drawNonce draws a boot nonce: random, non-zero, never persisted.
func drawNonce() uint64 {
	if mut("nonce_fixed") {
		return 1
	}
	for {
		var b [8]byte
		if _, err := crand.Read(b[:]); err != nil {
			panic("quorum: crypto/rand: " + err.Error())
		}
		if n := binary.BigEndian.Uint64(b[:]); n != 0 {
			return n
		}
	}
}

// jitter returns the candidate's retry jitter in [0, lease/4), seeded at
// random per boot.
func jitter(lease time.Duration) func() time.Duration {
	r := rand.New(rand.NewPCG(drawNonce(), drawNonce()))
	var mu sync.Mutex
	return func() time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return time.Duration(r.Int64N(int64(lease / 4)))
	}
}

// Lease returns the lease this elector runs.
func (e *Elector) Lease() time.Duration { return e.o.Lease }

// Nonce returns this boot's nonce (for tests and the status page; it is
// not a secret, and changes every start).
func (e *Elector) Nonce() uint64 { return e.nonce }

// SetHealth makes renewing and campaigning depend on h (#621): a look
// that finds h false closes the gate at once and sends no renewal, so the
// lease lapses, and a replica that is not live never campaigns. Call it
// before Run.
func (e *Elector) SetHealth(h func() bool) {
	e.mu.Lock()
	e.health = h
	e.mu.Unlock()
}

// FenceDefaults are the fence margin's parts for a lease when the
// manifest names none (design §3.4): 50 ms of network delay and 20 ms of
// send margin, scaled down for a short lease (at most lease/8 and
// lease/16), so the margin stays under a quarter of any lease.
func FenceDefaults(lease time.Duration) (netDelay, sendMargin time.Duration) {
	return min(50*time.Millisecond, lease/8), min(20*time.Millisecond, lease/16)
}

// Fence is the fence margin: the gate closes this long before the lease
// ends.
func (e *Elector) Fence() time.Duration {
	if mut("no_fence_margin") {
		return 0
	}
	return e.o.NetDelay + e.o.SendMargin
}

// IsLeader reports whether the write gate is open: this controller holds a
// majority lease with more than the fence margin left, judged against the
// clock now (not at the elector's last look, so a stalled elector can't
// hold the gate open past the lease), and its scans were live at its last
// look, and it hasn't released.
func (e *Elector) IsLeader() bool {
	ok, _, _ := e.SendGate()
	return ok
}

// SendGate is IsLeader with the budget a send that starts now has (the
// lease left minus the network delay) and the term it leads under (Term),
// read in one look (io.SendGate, design §3.4).
func (e *Elector) SendGate() (bool, time.Duration, uint64) {
	g := e.gate.Load()
	if mut("gate_on_tick") {
		return g.open, g.end.Sub(e.now()) - e.o.NetDelay, g.epoch
	}
	left := g.end.Sub(e.now())
	if !g.open || left <= e.Fence() {
		return false, 0, g.epoch
	}
	return true, left - e.o.NetDelay, g.epoch
}

// Term returns the last fencing token this controller led under: the term
// its current or last lease was acquired in, stable across renewals, 0
// before it first led. It only rises, as the Lease elector's does (the
// runtime's channels rely on it). A new lease after any gap has a new
// term, so the runtime takes over rather than resumes; a gate closed for
// unhealthy scans and opened again within the same lease keeps it, and
// the runtime resumes.
func (e *Elector) Term() uint64 { return e.gate.Load().epoch }

// IsTakeover reports whether the leadership this controller acquired
// last is a takeover from its peer: any but this boot's first, and that
// one too when its voter had seen the peer hold a lease before (#710 (b),
// runtime.TakeoverKnower). FALSE before any.
func (e *Elector) IsTakeover() bool { return e.tookOver.Load() }

// Decided is closed once this controller leads, or has seen the peer
// leading.
func (e *Elector) Decided() <-chan struct{} { return e.decided }

// Release closes the gate and stops renewing and campaigning, so the lease
// lapses and the peer takes over one lease later (after a stop: the voter
// still answers while the process lives).
func (e *Elector) Release() {
	e.mu.Lock()
	e.released = true
	e.publish(e.now())
	e.mu.Unlock()
}

// Run drives the elector until ctx ends: the disk writer, the looks, and
// the client-token check. Messages are answered only while it runs, and it
// returns once its writer has finished. Run it once.
func (e *Elector) Run(ctx context.Context) {
	e.mu.Lock()
	if e.ctx != nil {
		e.mu.Unlock()
		return
	}
	e.ctx = ctx
	pending := e.boxPending
	e.mu.Unlock()
	// No vote, no campaign and no write until the box can be read: a
	// clone booted where DNS fails must not vote as the original.
	if pending && !e.waitBox(ctx) {
		return
	}
	// Run returns only once its writer is done: nothing touches the state
	// file after it.
	var wg sync.WaitGroup
	for _, f := range []func(context.Context){e.w.run, e.emit, e.hello, e.probeLog} {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	defer wg.Wait()
	e.running.Store(true)
	defer e.running.Store(false)
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.wake:
		}
		t.Reset(e.look())
	}
}

// Close releases the state file's lock. Call it after Run has returned.
func (e *Elector) Close() {
	if e.free != nil {
		e.free()
		e.free = nil
	}
}

// uncommissionedEvery is how often, at most, an uncommissioned controller
// looks. Its look always sends the peer a query Adopt and the witness a
// term-0 probe, and every answer pokes the next look at once, so two
// uncommissioned controllers (a new pair waiting for commission, or both
// files lost) would ping-pong at network speed: some 30,000 requests a
// second each (#1029 3c-3c review). A controller with no incarnation holds
// no gate and grants nothing, so pacing it costs adoption latency only.
func uncommissionedEvery(lease time.Duration) time.Duration { return lease / 8 }

// pace returns how long an uncommissioned controller waits before its next
// look, 0 when it looks now (and notes the look).
func (e *Elector) pace() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.v.State().Incarnation != 0 || mut("uncommissioned_looks_unpaced") {
		return 0
	}
	now := e.now()
	if e.paced {
		if next := e.lookedAt.Add(uncommissionedEvery(e.o.Lease)); now < next {
			return next.Sub(now)
		}
	}
	e.lookedAt, e.paced = now, true
	return 0
}

// poke asks the loop for a look now: a reply may have won a lease, which
// renews at once, or moved the next look earlier.
func (e *Elector) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// look is one look of the candidate and the handshake. It returns how long
// until the next.
func (e *Elector) look() time.Duration {
	if e.mute() {
		return e.o.Lease // a spare looks at nothing and sends nothing (#1029 3c-2)
	}
	if wait := e.pace(); wait > 0 {
		return wait
	}
	e.mu.Lock()
	h, mf, epoch := e.health, e.markFn, e.c.Term()
	e.mu.Unlock()
	healthy := h == nil || h()
	ls, hasLog := e.logState() // outside mu: the log calls into the election
	var markSeq uint64
	var markCur bool
	if mf != nil && epoch != 0 {
		markSeq, markCur = mf(epoch) // outside mu, as the health check
	}

	e.mu.Lock()
	now := e.now()
	if mf != nil && e.c.Term() == epoch && e.c.Leading(now) {
		e.c.SetMark(markSeq, markCur)
	}
	from, until := e.v.LeaderSeen()
	e.unhealthy = !healthy
	e.maybeYield(now, ls, hasLog)
	may := e.mayCampaign(now) && (e.c.Leading(now) || !e.logHold(now, ls, hasLog))
	req := e.c.Tick(now, from, until, healthy && !e.released && !e.yielding(now) && !e.handingBack(now) && may)
	p, msgs, probe := e.a.Tick(now)
	peer := false // the probe goes to the peer's voter too
	if probe == nil {
		probe = e.c.Probe(now) // the handshake's own probe serves for both
		peer = probe != nil && quorum.ProbesPeer()
	} else {
		e.c.Probe(now)
		peer = quorum.AdoptProbesPeer()
	}
	var selfP *quorum.Persist
	if req != nil {
		// Its own voter first, in process.
		var rs []quorum.Reply
		selfP, rs = e.v.Handle(now, *req)
		e.route(now, rs)
		req.Promise = e.v.Relay(now)
	}
	if probe != nil {
		probe.Promise = e.v.Relay(now)
	}
	e.after(now)
	next := e.c.NextLook().Sub(now)
	e.mu.Unlock()

	e.w.ask(p)
	e.w.ask(selfP)
	if req != nil {
		e.vote(e.o.Peer, e.peer, *req)
		e.vote(e.o.Witness, e.wit, *req)
	}
	e.adopts(msgs)
	if probe != nil {
		e.vote(e.o.Witness, e.wit, *probe)
		if peer {
			e.vote(e.o.Peer, e.peer, *probe) // the peer's voter hears this boot too (#1029 step 1, 3c-3c-2b)
		}
	}
	return min(max(next, 0), e.o.Lease/8)
}

// route delivers replies the voter released: to its own candidate, or to
// the waiting request of the peer. Under mu.
func (e *Elector) route(now quorum.Instant, rs []quorum.Reply) {
	for _, r := range rs {
		if r.To == e.o.Self {
			e.noteMark(r)
			e.c.OnReply(now, r)
			continue
		}
		if ch, ok := e.waiters[keyFor(r)]; ok {
			delete(e.waiters, keyFor(r))
			ch <- r
		}
	}
}

// after drains the handshake's events and publishes the gate. Under mu.
func (e *Elector) after(now quorum.Instant) {
	for _, ev := range e.a.Events() {
		var out Event
		switch ev.Kind {
		case quorum.EventAdopted:
			e.needOp = false
			out = Event{Kind: EventAdopted, Gen: ev.Gen, Incarnation: ev.Incarnation}
			e.log.Info("redundancy: witness incarnation adopted", "gen", ev.Gen, "incarnation", ev.Incarnation)
		case quorum.EventTwoIncarnations:
			out = Event{Kind: EventTwoCopies, Incarnation: ev.Incarnation, Other: ev.Other}
			e.log.Error("redundancy: two witness incarnations answer: two copies of the witness are running", "incarnation", ev.Incarnation, "other", ev.Other)
		case quorum.EventRecommission:
			e.needOp = true
			out = Event{Kind: EventRecommissionNeeded, Gen: ev.Gen, Incarnation: ev.Incarnation}
			e.log.Error("redundancy: re-commission by hand: this controller is uncommissioned and its peer's committed witness incarnation has not answered it (or the peer is uncommissioned too); see the operator guide before acting", "peerGen", ev.Gen, "peerIncarnation", ev.Incarnation)
		case quorum.EventReadoptOverSwitch:
			out = Event{Kind: EventReadoptOverSwitch, Gen: ev.Gen, Incarnation: ev.Incarnation, Other: ev.Other}
			e.log.Warn("redundancy: re-adopted the peer's incarnation over a pending switch", "gen", ev.Gen, "incarnation", ev.Incarnation, "pending", ev.Other)
		default:
			continue
		}
		e.event(out)
	}
	e.publish(now)
	if from, until := e.v.LeaderSeen(); from == e.o.Peer && now < until {
		e.peerLeaseSeen = true
	}
	if t := e.c.Term(); e.c.Leading(now) && t != e.epoch {
		e.epoch = t
		e.acquired++
		// A takeover, not a start, unless this is the boot's first
		// leadership and it never saw the peer hold a lease (#710 (b)):
		// a whole-pair power-up starts, a standby that followed a live
		// leader takes over.
		e.tookOver.Store(e.acquired > 1 || e.peerLeaseSeen || mut("every_acquisition_a_takeover"))
		e.log.Info("redundancy: this controller leads", "term", t)
	}
	if from, until := e.v.LeaderSeen(); e.c.Leading(now) || (from == e.o.Peer && now < until) {
		e.once.Do(func() { close(e.decided) })
	}
}

// CountedIncarnation is the witness incarnation this controller's
// candidate counts a witness grant at (quorum.Candidate.Counting), 0 when
// none: the pair's log counts the witness's copy only at it (#710 3b-4b).
// Read without the lock, so the log may ask under its own.
func (e *Elector) CountedIncarnation() uint64 { return e.counted.Load() }

// publish stores the gate. Under mu.
func (e *Elector) publish(now quorum.Instant) {
	inc, from := e.c.Counting()
	if now < from {
		inc = 0 // its quiet period: its grants don't count yet, nor its log
	}
	e.counted.Store(inc)
	g := &gateState{}
	if old := e.gate.Load(); old != nil {
		g.epoch = old.epoch
	}
	if e.c.Leading(now) {
		g.epoch, g.end = max(g.epoch, e.c.Term()), e.c.LeaseEnd()
		g.open = (!e.released && !e.unhealthy && !e.handingBack(now)) || mut("gate_ignores_health")
		if e.spare.Load() {
			g.open = false
		}
	}
	e.gate.Store(g)
}

// event queues ev for OnEvent. Under mu; never blocks.
func (e *Elector) event(ev Event) { e.tryEvent(ev) }

// tryEvent is event, reporting whether ev was queued.
func (e *Elector) tryEvent(ev Event) bool {
	select {
	case e.events <- ev:
		return true
	default:
		e.log.Warn("redundancy: event dropped (the event handler is behind)", "kind", ev.Kind)
		return false
	}
}

func (e *Elector) emit(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.events:
			if e.o.OnEvent != nil {
				e.o.OnEvent(ev)
			}
		}
	}
}

// persisted is the writer's report of the write of seq.
func (e *Elector) persisted(seq uint64, err error) {
	var fix *quorum.Persist // a failed spare write's correction (#1144 review)
	e.mu.Lock()
	now := e.now()
	replies, msgs := e.a.Persisted(now, seq, err == nil || mut("controller_write_ignored"))
	e.route(now, replies)
	switch {
	case err != nil && e.writeErr == "":
		e.writeErr = err.Error()
		e.event(Event{Kind: EventStateWrite, Detail: e.writeErr})
		e.log.Error("redundancy: the voter state can't be written: this controller grants nothing until a write succeeds", "file", e.o.StatePath, "err", err)
	case err == nil && e.writeErr != "":
		e.writeErr = ""
		e.event(Event{Kind: EventStateWrite, Detail: "ok"})
		e.log.Info("redundancy: the voter state is written again", "file", e.o.StatePath)
	}
	if err == nil && seq > e.durable {
		e.durable = seq
	}
	if e.spareSeq != 0 && seq >= e.spareSeq {
		if err == nil {
			e.becomeSpare(now)
		} else {
			e.spareSeq = 0 // still held, not silent: it decides again on the next reply
			fix = e.v.SpareWriteFailed()
		}
	}
	close(e.written)
	e.written = make(chan struct{})
	e.after(now)
	silent := e.spare.Load()
	e.mu.Unlock()
	e.w.ask(fix)
	if !silent {
		e.adopts(msgs)
	}
	e.poke()
}

// vote sends r to a voter and hands its reply back.
func (e *Elector) vote(to quorum.NodeID, base string, r quorum.Request) {
	ctx := e.ctx
	if ctx == nil {
		return
	}
	b, _ := r.MarshalBinary()
	go func() {
		body, hdr, err := e.call.callHeader(ctx, e.o.Lease/8, http.MethodPost, base+Prefix+e.o.Cluster+"/vote", b)
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusConflict {
			switch se.reason {
			case "lease":
				e.mismatched(EventLeaseMismatch, string(to), se.said())
			case "term-jump":
				e.mismatched(EventTermJumpRefused, string(to), se.said())
			case "version":
				e.mismatched(EventVersionMismatch, string(to), se.said())
			}
		}
		if errors.As(err, &se) && se.code == http.StatusBadRequest {
			e.mismatched(EventVersionMismatch, string(to), olderPeerMsg(string(to)))
		}
		if (err == nil || (se != nil && se.code == http.StatusConflict && se.reason == "term-jump")) && voterLeaseFits(hdr, e.o.Lease) {
			// The voter answered past its lease check (a reply of any
			// kind, a probe's included, or a later refusal), and its own
			// lease takes this controller's (not only its restart wait's):
			// a lease refusal it gave clears.
			e.leaseTaken(string(to), false)
		}
		if err != nil || body == nil {
			return
		}
		var rep quorum.Reply
		if err := rep.UnmarshalBinary(body); err != nil {
			if errors.Is(err, quorum.ErrVersion) {
				e.mismatched(EventVersionMismatch, string(to), versionMsg(string(to), quorum.FrameVersion(body)))
			}
			return
		}
		if rep.From != to {
			return // a voter answers as itself, or not at all
		}
		e.reply(to, rep)
	}()
}

// reply takes a voter's reply to this controller's request.
func (e *Elector) reply(from quorum.NodeID, r quorum.Reply) {
	e.mu.Lock()
	now := e.now()
	if e.v.FencedReply(r) {
		e.mu.Unlock()
		return // a fenced state's voter: nothing it says counts (#1029 3c-3c-1b)
	}
	if e.v.LearnFences(r.Fences, e.o.Peer) {
		// Uncommissioned (a lost file): the fences the voter that answered
		// keeps, learnt before this same reply can make the witness heard
		// and the peer's State taken (#1164 review, B1/B2).
		e.w.ask(e.v.MarkWrite())
	}
	if !mut("reply_relay_dropped") {
		e.v.Hear(now, from, 0, r.Promise)
		e.noteDuplicates()
	}
	e.noteMark(r) // a probe's reply too: how a marked name hears it cleared
	spareP := e.noteHeld(now, r)
	if spareP != nil || e.spare.Load() {
		e.mu.Unlock()
		e.w.ask(spareP)
		return
	}
	if quorum.IsRelayProbe(r) {
		e.mu.Unlock()
		return // the relay probe's reply is for the voter only
	}
	e.c.OnReply(now, r)
	switch from {
	case e.o.Peer:
		e.a.OnPeer(now)
		e.peerAt, e.peerHeard = now, true
	case e.o.Witness:
		e.a.OnWitness(now, r)
		if r.To == e.o.Self && r.Incarnation != 0 {
			e.witInc, e.witAt, e.witHeard = r.Incarnation, now, true
		}
	}
	e.after(now)
	e.mu.Unlock()
	e.poke()
}

// noteLink hears every peer call's outcome (caller.note) and keeps the
// peer-TLS and identity reports about the voter it went to (#710): raised
// when a call shows the other protocol or a foreign key at that voter,
// cleared when a call to it gets an answer. An error that says nothing
// about TLS (a timeout, a refused connection) leaves them as they stand.
func (e *Elector) noteLink(url string, err error) {
	if strings.HasPrefix(url, e.peer+Prefix) && strings.HasSuffix(url, "/vote") {
		e.notePeerLink(err) // votes only: a hello's own failure must not count, or each vote would ask again
	}
	if mut("peer_tls_mismatch_unnamed") {
		return
	}
	var voter quorum.NodeID
	var who string
	switch {
	case strings.HasPrefix(url, e.peer+Prefix):
		voter, who = e.o.Peer, "controller "+string(e.o.Peer)
	case strings.HasPrefix(url, e.wit+Prefix):
		voter, who = e.o.Witness, "the witness"
	default:
		return
	}
	var tlsMsg, idMsg string
	switch faultOf(err) {
	case faultNone:
		if err != nil {
			return
		}
	case faultPeerPlain:
		tlsMsg = who + " answers plain HTTP on its peer port and this controller speaks TLS: peer TLS is off there (redundancy.peer-tls: off, or naut witness --peer-tls=off), it runs a release from before peer TLS, or something else answers at " + hostOf(e.baseOf(voter)) + ". Turn peer TLS on there, or upgrade it; never turn it off here to clear this, which would send the peer token to whatever answers"
	case faultPeerTLS:
		tlsMsg = who + " speaks TLS on its peer port and this controller plain HTTP (redundancy.peer-tls: off here): run one peer TLS setting on both controllers and the witness"
	case faultForeignKey:
		idMsg = who + "'s TLS key is not this pair's: it holds another NAUTILUS_PEER_TOKEN, or something else answers at " + hostOf(e.baseOf(voter)) + ". Give the two controllers and the witness the same token"
	case faultKeyRefused:
		idMsg = who + " refused this controller's TLS key: it holds another NAUTILUS_PEER_TOKEN. Give the two controllers and the witness the same token"
	}
	e.linkState(EventPeerTLS, voter, tlsMsg)
	e.linkState(EventPeerIdentity, voter, idMsg)
}

// hostOf is a base URL's host:port.
func hostOf(base string) string {
	_, h, ok := strings.Cut(base, "://")
	if !ok {
		return base
	}
	return h
}

// baseOf is the base URL of voter's peer port.
func (e *Elector) baseOf(voter quorum.NodeID) string {
	if voter == e.o.Witness {
		return e.wit
	}
	return e.peer
}

// linkState sets the standing report of kind about voter to msg ("" clears
// it): an event, and a log line, at each change only.
func (e *Elector) linkState(kind EventKind, voter quorum.NodeID, msg string) {
	key := string(kind) + "/" + string(voter)
	e.mu.Lock()
	changed := e.links[key] != msg
	// The state moves only with its event: a change whose event is
	// dropped (the handler behind) is tried again at the next call, so a
	// raise or a clear is never lost for good (#1230 review).
	if changed && e.tryEvent(Event{Kind: kind, Voter: voter, Detail: msg}) {
		if msg == "" {
			delete(e.links, key)
		} else {
			e.links[key] = msg
		}
	} else {
		changed = false
	}
	e.mu.Unlock()
	switch {
	case !changed:
	case msg != "":
		e.log.Error("redundancy: "+msg, "voter", string(voter))
	default:
		e.log.Info("redundancy: peer calls get through again", "voter", string(voter), "was", string(kind))
	}
}

// mismatched reports voter's refusal of this controller's request (a
// lease mismatch, a term jump, a version): logged and raised once per
// change of what it says.
func (e *Elector) mismatched(kind EventKind, voter, msg string) {
	e.refusal(kind, voter, false, msg)
}

// refused reports this controller's voter's refusal of voter's request,
// as mismatched does the other way.
func (e *Elector) refused(kind EventKind, voter, msg string) {
	e.refusal(kind, voter, true, msg)
}

func (e *Elector) refusal(kind EventKind, voter string, inbound bool, msg string) {
	e.mu.Lock()
	changed := e.noteRefusal(kind, voter, inbound, msg)
	e.mu.Unlock()
	if changed {
		e.logRefusal(kind, voter, inbound, msg)
	}
}

// refusalKey keys a refusal by its kind, the other side and the direction.
func refusalKey(kind EventKind, voter string, inbound bool) string {
	if inbound && !mut("lease_refusal_one_way") {
		return string(kind) + "/" + voter + "/in"
	}
	return string(kind) + "/" + voter
}

// noteRefusal moves a refusal's state and raises its event, under e.mu:
// msg "" clears it. As linkState, the state moves only with its event, so
// a raise or a clear the handler drops is tried again at the next call
// and never lost for good. It reports whether it moved.
func (e *Elector) noteRefusal(kind EventKind, voter string, inbound bool, msg string) bool {
	key := refusalKey(kind, voter, inbound)
	if e.mismatch[key] == msg {
		return false
	}
	if !e.tryEvent(Event{Kind: kind, Voter: quorum.NodeID(voter), Inbound: inbound, Detail: msg}) {
		return false
	}
	if msg == "" {
		delete(e.mismatch, key)
	} else {
		e.mismatch[key] = msg
	}
	return true
}

// leaseTaken clears a lease refusal between this controller and voter in
// one direction (inbound: this controller's voter took voter's lease),
// when one stands. Cheap when none does.
func (e *Elector) leaseTaken(voter string, inbound bool) {
	if mut("lease_mismatch_never_clears") {
		return
	}
	e.mu.Lock()
	changed := e.leaseTakenLocked(voter, inbound)
	e.mu.Unlock()
	if changed {
		e.logRefusal(EventLeaseMismatch, voter, inbound, "")
	}
}

func (e *Elector) leaseTakenLocked(voter string, inbound bool) bool {
	if _, ok := e.mismatch[refusalKey(EventLeaseMismatch, voter, inbound)]; !ok {
		return false
	}
	return e.noteRefusal(EventLeaseMismatch, voter, inbound, "")
}

func (e *Elector) logRefusal(kind EventKind, voter string, inbound bool, msg string) {
	switch {
	case msg == "" && inbound:
		e.log.Info("redundancy: this controller's voter takes the peer's lease again", "voter", voter, "was", string(kind))
	case msg == "":
		e.log.Info("redundancy: a voter takes this controller's lease again", "voter", voter, "was", string(kind))
	case kind == EventTermJumpRefused:
		e.log.Error("redundancy: a vote was refused as a term jump", "voter", voter, "inbound", inbound, "detail", msg)
	default:
		e.log.Error("redundancy: a vote was refused for its lease: the pair's leases (or the witness's --lease) disagree", "voter", voter, "inbound", inbound, "detail", msg)
	}
}

// adopts sends handshake messages to the peer.
func (e *Elector) adopts(msgs []quorum.Adopt) {
	ctx := e.ctx
	if ctx == nil {
		return
	}
	for _, m := range msgs {
		b, _ := m.MarshalBinary()
		go func() {
			_, _ = e.call.call(ctx, e.o.Lease/8, http.MethodPost, e.peer+Prefix+e.o.Cluster+"/adopt", b)
		}()
	}
}

// Handler serves the vote protocol to the peer. Mount it on the peer
// listener behind Guard, never behind the tag API.
func (e *Elector) Handler() http.Handler {
	mux := http.NewServeMux()
	base := Prefix + e.o.Cluster
	mux.HandleFunc("POST "+base+"/vote", e.serveVote)
	mux.HandleFunc("POST "+base+"/adopt", e.serveAdopt)
	mux.HandleFunc("GET "+base+"/hello", e.serveHello)
	mux.HandleFunc("POST "+base+"/dup", e.serveDup)
	return mux
}

func (e *Elector) serveVote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(VoterLeaseHeader, strconv.FormatInt(int64(e.o.Lease), 10))
	if !e.running.Load() || e.silent() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	b, err := readFrame(r)
	var req quorum.Request
	if err == nil && errors.Is(req.UnmarshalBinary(b), quorum.ErrVersion) {
		msg := versionMsg(string(e.o.Peer), quorum.FrameVersion(b))
		e.refused(EventVersionMismatch, string(e.o.Peer), msg)
		refuse(w, "version", msg)
		return
	}
	if err != nil || req.UnmarshalBinary(b) != nil {
		http.Error(w, "bad frame", http.StatusBadRequest)
		return
	}
	if req.From != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	stated := statedLease(r)
	e.mu.Lock()
	now := e.now()
	if limit := e.v.MaxRequestLease(now); !leaseFits(stated, limit) && !mut("controller_lease_unchecked") {
		e.mu.Unlock()
		msg := fmt.Sprintf("%s's lease %v is longer than this voter's %v (or missing)", req.From, stated, limit)
		e.refused(EventLeaseMismatch, string(req.From), msg)
		refuse(w, "lease", msg)
		return
	}
	leaseBack := !mut("lease_mismatch_never_clears") && e.leaseTakenLocked(string(req.From), true)
	if st := e.v.State(); termJump(req.Term, st.Promised, st.Floor) && !mut("controller_no_term_cap") {
		e.mu.Unlock()
		msg := fmt.Sprintf("%s asked for term %d, more than %d above the %d this voter promised", req.From, req.Term, uint64(MaxTermJump), max(st.Promised, st.Floor))
		e.refused(EventTermJumpRefused, string(req.From), msg)
		refuse(w, "term-jump", msg)
		if leaseBack {
			e.logRefusal(EventLeaseMismatch, string(req.From), true, "")
		}
		return
	}
	if fenced := e.v.FencedRequest(req); !fenced {
		// A fenced state's frame (an old box an operator replaced) is
		// only refused (#1029 3c-3c-1b).
		e.v.LearnFences(req.Fences, e.o.Peer) // its write goes with Handle's (MarkWrite)
		if !(req.Term == 0 && req.ID == quorum.ProbeID) {
			// A relay probe is for the voter only (#1029 step 1): the
			// handshake keeps its own idea of hearing the peer.
			e.a.OnPeer(now)
			e.peerAt, e.peerHeard = now, true
		}
		e.v.Hear(now, req.From, req.Nonce, req.Promise)
		if req.Nonce != e.peerVoteNonce {
			// The peer's first vote, or a new boot of it: its hello
			// answer may say something new (its API address among it).
			e.peerVoteNonce = req.Nonce
			defer e.hellosAgain()
		}
		e.c.Observe(req.Term)
		quorum.YieldTo(now, e.v, e.c, req)
	}
	p, replies := e.v.Handle(now, req)
	var push *quorum.Request
	if e.v.RelayChanged(now) {
		push = e.c.PushProbe(now) // the witness hears this new promise now, not at the next look
		if push != nil {
			push.Promise = e.v.Relay(now)
		}
	}
	e.noteDuplicates()
	var ch chan quorum.Reply
	if len(replies) == 0 && p != nil {
		ch = make(chan quorum.Reply, 1)
		e.waiters[keyOf(req)] = ch
	}
	e.after(now)
	e.mu.Unlock()
	if leaseBack {
		e.logRefusal(EventLeaseMismatch, string(req.From), true, "")
	}
	e.w.ask(p)
	if push != nil {
		e.vote(e.o.Witness, e.wit, *push)
	}
	e.poke()
	answer(w, r, replies, ch, e.o.Lease, e.stamp, func() {
		e.mu.Lock()
		if e.waiters[keyOf(req)] == ch {
			delete(e.waiters, keyOf(req))
		}
		e.mu.Unlock()
	})
}

// noteDuplicates raises redundancy.duplicate-member when the voter's
// count of duplicates rose, at most once per dupEvery: from a request it
// refused, or a relay it heard on a reply (#1029 layer 2). Under mu.
func (e *Elector) noteDuplicates() {
	n, who := e.v.Duplicates()
	if msg := confirmedAlarm(e.v, &e.markSeen); msg != "" {
		e.dupSeen = n // the mark's own count, reported here
		e.event(Event{Kind: EventDuplicateMember, Detail: msg})
		e.log.Error("redundancy: " + msg)
		return
	}
	if n == e.dupSeen {
		return
	}
	e.dupSeen = n
	if t := time.Now(); t.Sub(e.dupAt) >= dupEvery {
		e.dupAt = t
		msg := duplicateMsg(who, e.v.DuplicateCause())
		e.event(Event{Kind: EventDuplicateMember, Detail: msg})
		e.log.Error("redundancy: " + msg)
	}
}

// noteMark hands a reply meant for this boot to the candidate's mark
// (quorum.Candidate.NoteMark) and tells the operator when this
// controller starts or stops being held by one. Under mu.
func (e *Elector) noteMark(r quorum.Reply) {
	if r.To != e.o.Self || r.Nonce != e.nonce {
		return
	}
	was := e.c.Marked()
	e.c.NoteMark(r)
	if by := e.c.Superseded(); by != "" && !e.supersededTold {
		e.supersededTold = true
		msg := supersededMsg(e.o.Self, by)
		e.event(Event{Kind: EventSuperseded, Detail: msg})
		e.log.Error("redundancy: " + msg)
	}
	switch now := e.c.Marked(); {
	case now && !was:
		msg := fmt.Sprintf("voter %s marks %s a confirmed duplicate: another process runs as this controller, so it campaigns no more, and its voter grants %s nothing new, until the mark is cleared. Clear it: (1) stop the copy and confirm it is down; (2) run naut redundancy clear-duplicate %s --copy-stopped (on either controller): the hold ends without a restart. A restart of this controller or of a marking member doesn't end it", r.From, e.o.Self, e.o.Peer, e.o.Self)
		e.event(Event{Kind: EventDuplicateMarked, Detail: msg})
		e.log.Error("redundancy: " + msg)
	case was && !now:
		e.event(Event{Kind: EventDuplicateMarked})
		e.log.Info("redundancy: no voter marks this controller a duplicate any more; it may campaign again")
	}
}

// noteHeld hands a reply meant for this boot to the candidate's spare
// rule (quorum.Candidate.NoteHeld) and, when both other voters hold
// another boot of this name the incumbent, decides: the flag's write is
// returned for the writer, and the controller goes silent only once it is
// durable (persisted). Until then it is the held copy it was. Under mu.
func (e *Elector) noteHeld(now quorum.Instant, r quorum.Reply) *quorum.Persist {
	if e.spare.Load() || r.To != e.o.Self || r.Nonce != e.nonce {
		return nil
	}
	e.c.NoteHeld(now, r)
	if e.spareSeq != 0 || !e.c.Spare(now) {
		return nil
	}
	p := e.v.MakeSpare()
	e.spareSeq = p.Seq
	e.log.Warn("redundancy: both other members hold another process of this name: this one is a copy, and makes itself a spare once that is written down")
	if mut("spare_flag_after_silence") {
		e.becomeSpare(now)
	}
	return p
}

// becomeSpare silences this controller: its spare flag is durable. Under
// mu.
func (e *Elector) becomeSpare(now quorum.Instant) {
	e.spareSeq = 0
	if e.spare.Swap(true) {
		return
	}
	for k := range e.waiters {
		delete(e.waiters, k) // an answer still held for a write is never sent
	}
	e.publish(now)
	msg := spareMsg(e.o.Self)
	e.event(Event{Kind: EventSpare, Detail: msg})
	e.log.Error("redundancy: " + msg)
}

// Spare reports whether this controller is a spare (#1029 3c-2): silent
// until an operator acts.
func (e *Elector) Spare() bool { return e.spare.Load() }

// Unspare clears the spare flag on disk. Tests use it; the operator's
// recommission --from-spare will (3c-3). The controller stays silent
// until it is restarted, and starts then as the member its file says.
func (e *Elector) Unspare(ctx context.Context) error {
	e.mu.Lock()
	p := e.v.Unspare()
	e.mu.Unlock()
	if p == nil {
		return nil
	}
	e.w.ask(p)
	return e.waitDurable(ctx, p.Seq)
}

// DuplicateMarks lists the names this controller's voter has marked as
// run by two processes at once (quorum.Voter.Marked), sorted.
func (e *Elector) DuplicateMarks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, n := range e.v.Marked() {
		out = append(out, string(n))
	}
	return out
}

// stamp sets the voter's running promise on a reply as it leaves
// (quorum.Voter.Relay): a grant held for its write leaves later than
// Handle made it.
func (e *Elector) stamp(rep quorum.Reply) quorum.Reply {
	e.mu.Lock()
	rep.Promise = e.v.Relay(e.now())
	e.mu.Unlock()
	return rep
}

// answer writes a voter's reply: the one Handle gave at once, or the grant
// its write releases, waited for at most a lease; 204 when there is none.
// stamp sets the voter's relayed promise as the reply leaves.
func answer(w http.ResponseWriter, r *http.Request, replies []quorum.Reply, ch chan quorum.Reply, lease time.Duration, stamp func(quorum.Reply) quorum.Reply, drop func()) {
	if len(replies) > 0 {
		b, _ := stamp(replies[0]).MarshalBinary()
		writeFrame(w, b)
		return
	}
	if ch == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	t := time.NewTimer(lease)
	defer t.Stop()
	select {
	case rep := <-ch:
		b, _ := stamp(rep).MarshalBinary()
		writeFrame(w, b)
		return
	case <-r.Context().Done():
	case <-t.C:
	}
	drop()
	w.WriteHeader(http.StatusNoContent)
}

func (e *Elector) serveAdopt(w http.ResponseWriter, r *http.Request) {
	if !e.running.Load() || e.silent() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	b, err := readFrame(r)
	var m quorum.Adopt
	if err == nil && errors.Is(m.UnmarshalBinary(b), quorum.ErrVersion) {
		msg := versionMsg(string(e.o.Peer), quorum.FrameVersion(b))
		e.mismatched(EventVersionMismatch, string(e.o.Peer), msg)
		refuse(w, "version", msg)
		return
	}
	if err != nil || m.UnmarshalBinary(b) != nil {
		http.Error(w, "bad frame", http.StatusBadRequest)
		return
	}
	if m.From != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	e.mu.Lock()
	now := e.now()
	fenced := e.a.Fenced(m)
	p, msgs := e.a.OnAdopt(now, m)
	if !fenced {
		e.peerAt, e.peerHeard = now, true // a fenced state's Adopt is no word from the peer (#1029 3c-3c-1b)
	}
	e.after(now)
	e.mu.Unlock()
	e.w.ask(p)
	e.adopts(msgs)
	e.poke()
	w.WriteHeader(http.StatusNoContent)
}

func (e *Elector) serveHello(w http.ResponseWriter, r *http.Request) {
	if e.silent() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	c := r.URL.Query().Get("c")
	if len(c) < 16 || len(c) > 128 {
		http.Error(w, "challenge", http.StatusBadRequest)
		return
	}
	if quorum.NodeID(r.URL.Query().Get("from")) != e.o.Peer {
		http.Error(w, "not this pair's peer", http.StatusForbidden)
		return
	}
	if pi, ok := infoFromQuery(r.URL.Query()); ok {
		e.heard(pi)
	}
	self := e.selfInfo()
	if r.URL.Query().Get("log") == "1" {
		self.Log = e.selfLog() // outside mu
	}
	w.Header().Set("Content-Type", "application/json")
	cr := e.credentials()
	us := credOK
	if cr.UsersErr != "" {
		us = credUnreadable
	}
	cs := credOK
	if cr.ContactsErr != "" {
		cs = credUnreadable
	}
	_ = json.NewEncoder(w).Encode(helloAnswer{Node: string(e.o.Self), MAC: tokenMAC(e.o.Token, c, e.o.Peer, e.o.Self, e.o.ClientToken),
		Config: self.Config, Nonce: strconv.FormatUint(self.Nonce, 16), Caps: self.Caps, Log: self.Log, API: self.API,
		InfoMAC: infoMAC(e.o.Token, c, e.o.Peer, e.o.Self, self),
		Users:   us, UsersMAC: credMAC(e.o.Token, c, e.o.Peer, e.o.Self, "users", us, cr.Users),
		TLS: credOK, TLSMAC: credMAC(e.o.Token, c, e.o.Peer, e.o.Self, "tls", credOK, cr.TLS),
		Contacts: cs, ContactsMAC: credMAC(e.o.Token, c, e.o.Peer, e.o.Self, contactsPart(), cs, cr.Contacts)})
}

// silent: a spare answers nobody, as if it were down (#1029 3c-2).
func (e *Elector) silent() bool { return e.spare.Load() && !mut("spare_answers_votes") }

// mute: a spare sends nothing, no vote, probe or hello: one that kept
// probing would stay the fresh boot of its name at the voters, and an
// original that restarts would then be named a copy of it (#1029 3c-2).
func (e *Elector) mute() bool { return e.spare.Load() && !mut("spare_still_looks") }

// credentials reads this controller's credentials now (Options.Credentials).
func (e *Elector) credentials() Credentials {
	if e.o.Credentials == nil {
		return Credentials{}
	}
	return e.o.Credentials()
}

// helloEvery is the hello's interval: 16 leases, at least 5 s.
func helloEvery(lease time.Duration) time.Duration { return max(16*lease, 5*time.Second) }

// hello runs the client-token check against the peer: at start and every
// 16 leases (at least every 5 s), raising EventClientToken when the
// answer changes.
//
// A hello that gets no verified answer is retried sooner (#1120): the
// standby's leader API address, its credential checks and the peer's
// configuration all come only from a verified answer, and with a peer
// that started after this controller (both powered up together, or the
// peer restarted) a whole interval would refuse the standby's writes
// instead of forwarding them for up to 16 leases. The retry starts at
// helloRetry and doubles up to the interval, so a dead peer is not asked
// (a TLS handshake each time) every second for ever. A link to the peer
// that comes up, or a new boot of the peer heard in its votes, sends a
// hello at once and starts the retries over (hellosAgain).
//
// The ask is spaced too: a hello it sends comes at least helloRetry after
// the one before, so two processes behind the peer's name (their votes
// alternating boot nonces) or a link that flaps at every other call send
// a hello a lease or so at most, never one after another (#1120 review).
func (e *Elector) hello(ctx context.Context) {
	every := helloEvery(e.o.Lease)
	first := helloRetry(e.o.Lease)
	retry := first
	for {
		sent := time.Now()
		wait := every
		if !e.checkToken(ctx) && !mut("hello_retry_slow") {
			wait = retry
			retry = min(2*retry, every)
		} else {
			retry = first
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-e.helloNow:
			t.Stop()
			retry = first
			if gap := time.Until(sent.Add(first)); gap > 0 && !mut("hello_asks_unspaced") {
				t = time.NewTimer(gap)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
		}
	}
}

// helloRetry is the first retry after a hello with no verified answer:
// a lease, at least a second.
func helloRetry(lease time.Duration) time.Duration { return max(lease, time.Second) }

// hellosAgain asks the hello loop for a hello now, if it is not asked
// already.
func (e *Elector) hellosAgain() {
	if mut("hello_not_asked_again") {
		return
	}
	select {
	case e.helloNow <- struct{}{}:
	default:
	}
}

// notePeerLink follows the link to the peer from its vote calls'
// outcomes: one answered after one that was not is the link coming up
// (the peer started, or a cut healed), and the hello goes at once.
func (e *Elector) notePeerLink(err error) {
	e.mu.Lock()
	up := err == nil && e.peerDown
	e.peerDown = err != nil
	e.mu.Unlock()
	if up {
		e.hellosAgain()
	}
}

// checkToken sends one hello and reports whether it got a verified
// answer.
func (e *Elector) checkToken(ctx context.Context) bool {
	if e.mute() {
		return true // a spare says nothing to its peer, its boot nonce included: nothing to retry
	}
	c := newChallenge()
	q := url.Values{"c": {c}, "from": {string(e.o.Self)}}
	e.selfInfo().query(q)
	body, err := e.call.call(ctx, max(e.o.Lease, time.Second), http.MethodGet, e.peer+Prefix+e.o.Cluster+"/hello?"+q.Encode(), nil)
	if err != nil {
		e.credentialsUnknown() // the check can't run: no stale banner
		return false
	}
	var h helloAnswer
	if json.Unmarshal(body, &h) != nil {
		e.credentialsUnknown()
		return false
	}
	if h.Node != string(e.o.Peer) {
		e.log.Error("redundancy: the peer's address answers as another node", "addr", e.o.PeerAddr, "want", e.o.Peer, "got", h.Node)
		e.credentialsUnknown()
		return false
	}
	pi, verified := e.answerVerified(c, h)
	if verified {
		e.heard(pi)
		e.heardAPI(pi.API)
	}
	e.checkCredentials(c, h, verified)
	got := TokenDiffers
	if sameMAC(h.MAC, tokenMAC(e.o.Token, c, e.o.Self, e.o.Peer, e.o.ClientToken)) {
		got = TokenSame
	}
	e.mu.Lock()
	changed := got != e.token
	e.token = got
	if changed {
		e.event(Event{Kind: EventClientToken, Detail: got.String()})
	}
	e.mu.Unlock()
	switch {
	case changed && got == TokenDiffers:
		e.log.Error("redundancy: client token differs between " + string(e.o.Self) + " and " + string(e.o.Peer) + ": the pair is one controller to its clients, so both need the same NAUTILUS_TOKEN")
	case changed:
		e.log.Info("redundancy: the peer holds the same client token")
	}
	return verified
}

// checkCredentials judges the peer's credential answer for challenge c
// against this controller's own, read now (so a users file edited on one
// box shows within one hello), and reports each part whose message
// changed: an event (the banner's condition) and a log line. verified says
// the answer proved itself under the peer token (judgeCred). Report only
// (#708): nothing here holds a campaign or touches the gate.
func (e *Elector) checkCredentials(c string, h helloAnswer, verified bool) {
	own := e.credentials()
	users := judgeCred(e.o.Token, c, e.o.Self, e.o.Peer, "users", h.Users, h.UsersMAC, own.Users, own.UsersErr, verified)
	tls := judgeCred(e.o.Token, c, e.o.Self, e.o.Peer, "tls", h.TLS, h.TLSMAC, own.TLS, "", verified)
	if users == CredUnreadable && own.UsersErr != "" {
		// The detail stays in this controller's log: /api/cluster, where
		// the condition shows, answers without a token (#1046 review).
		e.log.Error("redundancy: this controller cannot read its users file, so the pair's users cannot be compared", "error", own.UsersErr)
	}
	e.reportCredentials(users, e.usersMessage(users, own.UsersErr), tls, e.tlsMessage(tls))
	contacts := judgeCred(e.o.Token, c, e.o.Self, e.o.Peer, contactsPart(), h.Contacts, h.ContactsMAC, own.Contacts, own.ContactsErr, verified)
	if contacts == CredUnreadable && own.ContactsErr != "" {
		e.log.Error("redundancy: this controller cannot read its alarm contacts, so the pair's cannot be compared", "error", own.ContactsErr)
	}
	e.reportContacts(contacts, e.contactsMessage(contacts, own.ContactsErr))
}

// contactsPart is the contacts' credMAC part: its own label, so a users or
// TLS MAC never verifies as contacts.
func contactsPart() string {
	if mut("contacts_part_users") {
		return "users"
	}
	return "contacts"
}

// reportContacts records the contacts check and raises its event when its
// message changed ("" clears the condition). Report only.
func (e *Elector) reportContacts(st CredState, msg string) {
	e.mu.Lock()
	changed := msg != e.contactsMsg
	e.contacts = st
	if changed {
		e.contactsMsg = msg
		e.event(Event{Kind: EventContactsDiffer, Detail: msg})
	}
	e.mu.Unlock()
	switch {
	case changed && msg != "":
		e.log.Warn("redundancy: " + msg)
	case changed:
		e.log.Info("redundancy: the alarm contacts condition cleared", "state", st.String())
	}
}

// contactsMessage is the operator's text for the alarm contacts' state (""
// when they match, neither has any, or nothing is known). Never a path,
// an address or an OS error: /api/cluster shows it without a token.
func (e *Elector) contactsMessage(st CredState, ownErr string) string {
	switch st {
	case CredDiffers:
		return "alarm contacts differ from the peer: " + string(e.o.Self) + " and " + string(e.o.Peer) + " hold different alarm-contacts.json or alarm-secrets.json (or one has none). Copy both files from the controller that has the right ones into the other's state directory (scp -p, keeping them mode 0600 and owned by the service user; each reads them at its next page), or set the same entries on both with naut alarms contacts set and naut alarms secrets set. Until then, after a takeover a page may go to an old address, or not at all"
	case CredUnreadable:
		if ownErr != "" {
			return "this controller (" + string(e.o.Self) + ") cannot read its alarm contacts, so the pair's cannot be compared: see its log"
		}
		return "the peer controller (" + string(e.o.Peer) + ") cannot read its alarm contacts, so the pair's cannot be compared: see its log"
	case CredUnverified:
		return e.unverifiedMessage("alarm contacts")
	}
	return ""
}

// answerVerified reports whether a hello answer proved itself under the
// peer token for challenge c, from the peer to this controller (its
// peer-info MAC binds both names and the challenge), with the peer info it
// carries.
func (e *Elector) answerVerified(c string, h helloAnswer) (peerInfo, bool) {
	n, err := strconv.ParseUint(h.Nonce, 16, 64)
	if err != nil || n == 0 {
		return peerInfo{}, false
	}
	pi := peerInfo{Config: h.Config, Nonce: n, Caps: h.Caps, Log: h.Log, API: h.API}
	return pi, sameMAC(h.InfoMAC, infoMAC(e.o.Token, c, e.o.Self, e.o.Peer, pi))
}

// credentialsUnknown is the check that could not run (the peer did not
// answer, or answered as another node): a condition raised earlier is
// cleared rather than left standing on a state nobody re-checks.
func (e *Elector) credentialsUnknown() {
	e.reportCredentials(CredUnknown, "", CredUnknown, "")
	e.reportContacts(CredUnknown, "")
}

// reportCredentials records each part's state and raises its event when
// its message changed ("" clears the condition).
func (e *Elector) reportCredentials(users CredState, uMsg string, tls CredState, tMsg string) {
	e.mu.Lock()
	uChanged, tChanged := uMsg != e.usersMsg, tMsg != e.tlsMsg
	e.users, e.tls = users, tls
	if uChanged {
		e.usersMsg = uMsg
		e.event(Event{Kind: EventUsersDiffer, Detail: uMsg})
	}
	if tChanged {
		e.tlsMsg = tMsg
		e.event(Event{Kind: EventTLSDiffers, Detail: tMsg})
	}
	e.mu.Unlock()
	if uChanged {
		if uMsg != "" {
			e.log.Error("redundancy: " + uMsg)
		} else {
			e.log.Info("redundancy: the users condition cleared", "state", users.String())
		}
	}
	if tChanged {
		if tMsg != "" {
			e.log.Warn("redundancy: " + tMsg)
		} else {
			e.log.Info("redundancy: the TLS condition cleared", "state", tls.String())
		}
	}
}

// unverifiedMessage is the condition for an answer that did not prove
// itself: the files are not blamed.
func (e *Elector) unverifiedMessage(part string) string {
	return "the peer's " + part + " answer could not be verified, so the pair's " + part + " cannot be compared: " + string(e.o.Self) + " and " + string(e.o.Peer) + " may hold different NAUTILUS_PEER_TOKENs, or something else answers in the peer's place"
}

// usersMessage is the operator's text for a users-file state ("" when the
// files match, neither has users, or nothing is known). It never carries
// a path or an OS error: /api/cluster shows it without a token.
func (e *Elector) usersMessage(st CredState, ownErr string) string {
	switch st {
	case CredDiffers:
		return "the users files differ between " + string(e.o.Self) + " and " + string(e.o.Peer) + ": copy the same users.json to both controllers (scp it into the state directory; each reloads it within a second), and never add users separately on each box: each box salts its own hashes, so the same password still makes a different file. Until then a user may be refused, or still let in, after a takeover"
	case CredUnreadable:
		if ownErr != "" {
			return "this controller (" + string(e.o.Self) + ") cannot read its users file, so the pair's users cannot be compared: see its log"
		}
		return "the peer controller (" + string(e.o.Peer) + ") cannot read its users file, so the pair's users cannot be compared: see its log"
	case CredUnverified:
		return e.unverifiedMessage("users")
	}
	return ""
}

// tlsMessage is the operator's text for a TLS state ("" when it matches,
// neither serves TLS, or nothing is known).
func (e *Elector) tlsMessage(st CredState) string {
	switch st {
	case CredDiffers:
		return "the tag API's TLS certificates differ between " + string(e.o.Self) + " and " + string(e.o.Peer) + ": expected for a few minutes while a renewed certificate is copied to both; otherwise copy the same certificate and key to both controllers, or a standby cannot forward to the leader"
	case CredUnverified:
		return e.unverifiedMessage("TLS certificate")
	}
	return ""
}

// Recommission is the operator's way out of EventRecommissionNeeded
// (quorum.Adoption.Recommission, design §7): adopt incarnation inc, with
// every term at or below floor never granted again. It returns once the
// floor is durable, or the error; the adoption itself then goes through
// the peer. The permission lasts this boot: after a restart, issue it
// again, above KnownFloor.
func (e *Elector) Recommission(ctx context.Context, inc, floor uint64) error {
	_, err := e.RecommissionBy(ctx, "operator", inc, floor)
	return err
}

// RecommissionBy is Recommission naming the person, for the log's vouch
// (SetVouch), which runs first. A vouch the log refuses never refuses the
// re-commission (#1053 review: control never waits on operator data): it
// goes ahead, the log stays unsynced and read-only, and the refusal is
// returned as vouchRefused, raised as EventVouchRefused and logged.
//
// A controller whose state was written on another box (Status.BoxChanged)
// is refused (ErrBoxChanged): RecommissionChecked takes the operator's
// word that the box was replaced, not cloned (#1029 3c-3c).
func (e *Elector) RecommissionBy(ctx context.Context, by string, inc, floor uint64) (vouchRefused, err error) {
	// The members' fences, as RecommissionChecked takes them: one that
	// does not answer adds none (#1164 review).
	return e.recommission(ctx, by, inc, floor, false, maxFence(e.membersState(ctx)), LogAccept{}, nil)
}

// fence is the highest fence any member keeps (MemberState.Fence): the
// adoption the re-commission makes is at or above it (#1029 3c-3c-1b).
// acc is what the person accepted for the log's vouch, and pic, when not
// nil, gets the picture the vouch saw (#1029 3c-3c-2a).
func (e *Elector) recommission(ctx context.Context, by string, inc, floor uint64, boxReplaced bool, fence uint64, acc LogAccept, pic *json.RawMessage) (vouchRefused, err error) {
	// Its own refusals first, so a refused re-commission vouches for
	// nothing.
	e.mu.Lock()
	commissioned, known, boxChanged := e.v.State().Incarnation != 0, e.a.KnownFloor(), e.boxChanged
	e.mu.Unlock()
	switch {
	case e.spare.Load():
		return nil, ErrSpare
	case boxChanged != "" && !boxReplaced && !mut("box_replaced_unchecked"):
		return nil, fmt.Errorf("%w (%s): re-commission it only if its box was replaced, saying so (--box-replaced); a copy of a running controller must be stopped", ErrBoxChanged, boxChanged)
	case commissioned:
		return nil, quorum.ErrCommissioned
	case inc == 0 || floor == 0:
		return nil, quorum.ErrNoFloor
	case floor <= known && !mut("recommission_any_floor"):
		return nil, fmt.Errorf("%w: it knows terms up to %d", quorum.ErrFloorTooLow, known)
	case floor-known > MaxTermJump/2:
		return nil, fmt.Errorf("quorum: re-commission floor %d is more than %d above the terms this controller knows (%d): the voters would refuse the pair's next campaign as a term jump", floor, MaxTermJump/2, known)
	}
	vp, verr := e.vouch(ctx, by, inc, acc)
	if pic != nil {
		*pic = vp
	}
	if verr != nil {
		if mut("vouch_refusal_blocks") {
			return nil, fmt.Errorf("quorum: re-commission refused by the operator-intent log: %w", verr)
		}
		vouchRefused = verr
		msg := "the operator-intent log was not vouched for, so it stays unsynced and read-only: " + verr.Error()
		e.log.Error("redundancy: re-commissioning goes ahead, but " + msg)
		e.mu.Lock()
		e.event(Event{Kind: EventVouchRefused, Detail: msg})
		e.mu.Unlock()
	}
	e.mu.Lock()
	if known := e.a.KnownFloor(); floor > known && floor-known > MaxTermJump/2 {
		e.mu.Unlock()
		return vouchRefused, fmt.Errorf("quorum: re-commission floor %d is more than %d above the terms this controller knows (%d): the voters would refuse the pair's next campaign as a term jump", floor, MaxTermJump/2, known)
	}
	if !mut("recommission_under_fence") {
		e.a.ProposeAtLeast(fence)
	}
	p, err := e.a.Recommission(inc, floor)
	if err != nil {
		known := e.a.KnownFloor()
		e.mu.Unlock()
		if errors.Is(err, quorum.ErrFloorTooLow) {
			return vouchRefused, fmt.Errorf("%w: it knows terms up to %d", err, known)
		}
		return vouchRefused, err
	}
	e.mu.Unlock()
	e.w.ask(p)
	if err := e.waitDurable(ctx, p.Seq); err != nil {
		return vouchRefused, err
	}
	e.mu.Lock()
	e.event(Event{Kind: EventRecommissioned, Incarnation: inc, Detail: fmt.Sprintf("floor %d", floor)})
	e.mu.Unlock()
	e.log.Warn("redundancy: re-commissioned by an operator", "incarnation", inc, "floor", floor)
	e.poke()
	return vouchRefused, nil
}

// waitDurable waits until a write covering seq is on disk.
func (e *Elector) waitDurable(ctx context.Context, seq uint64) error {
	for {
		e.mu.Lock()
		done, ch, werr := e.durable >= seq, e.written, e.writeErr
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			if werr != "" {
				return fmt.Errorf("quorum: the state file can't be written: %s", werr)
			}
			return ctx.Err()
		case <-ch:
		}
	}
}

// KnownFloor is the highest term this controller knows was promised: an
// operator's Recommission floor must be above it.
func (e *Elector) KnownFloor() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.a.KnownFloor()
}

// ImageTermOK is the term check on a state image from the peer (#710
// S1b): quorum.ImageTermOK on this controller's voter, now. ownLed is the
// last term this controller led (its runtime's), seen the newest term it
// already took an image of. A spare takes none.
func (e *Elector) ImageTermOK(term, ownLed, seen uint64) (bool, string) {
	if e.spare.Load() {
		return false, "this controller is a spare"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return quorum.ImageTermOK(e.v, e.now(), e.o.Peer, term, ownLed, seen)
}

// SetSyncMark sets how this leader learns how far its standby is in sync
// (#710 S2c-1): f(epoch) is the sequence of the last state image the
// standby acknowledged in that epoch, and whether that is current. Each
// look asks it, outside the election's lock (f must not call into the
// election), and the renewals carry it to the voters
// (quorum.Candidate.SetMark), which keep it for the next takeover's hot
// check.
func (e *Elector) SetSyncMark(f func(epoch uint64) (seq uint64, current bool)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.markFn = f
}

// HotVerdict is the hot takeover's check against the voters (#710 S2c-1,
// quorum.HotCheck.Verdict) for an image of the peer at (epoch, seq), on
// the lease this controller holds now: hot, or the cold reason. Not
// leading, it is cold.
func (e *Elector) HotVerdict(epoch, seq uint64) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.c.Leading(e.now()) {
		return false, quorum.ColdNotLeading
	}
	return e.c.HotCheck().Verdict(epoch, seq)
}

// Status returns a snapshot.
func (e *Elector) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	st := e.v.State()
	s := Status{
		Self: string(e.o.Self), Peer: string(e.o.Peer),
		Commissioned: st.Incarnation != 0, Gen: st.Gen, Incarnation: st.Incarnation,
		PendingGen: st.PendingGen, PendingInc: st.PendingInc, Floor: st.Floor, KnownFloor: e.a.KnownFloor(),
		WitnessIncarnation: e.witInc, WitnessAge: -1, PeerAge: -1,
		RecommissionNeeded: e.needOp && st.Incarnation == 0, ClientToken: e.token, Users: e.users, TLS: e.tls, Contacts: e.contacts,
		StateWriteError: e.writeErr, Acquisitions: e.acquired, Duplicates: e.dupSeen,
		ConfigDiffers: e.cfgDiffers, HelloDuplicates: e.helloDups, BoxChanged: e.boxChanged, BoxPending: e.boxPending,
	}
	g := e.gate.Load()
	s.Leading = g.open && g.end.Sub(now) > e.Fence()
	s.Term = g.epoch
	s.Spare = e.spare.Load()
	s.FromSpare = e.fromSpare
	for _, n := range e.v.Marked() {
		s.Marked = append(s.Marked, string(n))
	}
	s.Held = e.c.Marked()
	s.Superseded = string(e.c.Superseded())
	switch from, until := e.v.LeaderSeen(); {
	case s.Spare:
		// A spare hears nobody and names no leader.
	case s.Leading:
		s.Leader, s.LeaderAddr = s.Self, e.o.SelfAPI
	case from == e.o.Peer && now < until:
		s.Leader, s.LeaderAddr = s.Peer, e.peerAPI
		if mut("peer_api_guessed") {
			s.LeaderAddr = guessPeerAPI(e.o.PeerAddr, e.o.SelfAPI)
		}
	}
	if e.witHeard {
		s.WitnessAge = now.Sub(e.witAt)
	}
	if e.peerHeard {
		s.PeerAge = now.Sub(e.peerAt)
	}
	return s
}
