package quorum

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"time"
)

// NodeID names a voter: a controller or the witness.
type NodeID string

// Request asks the voters for a term: a campaign (a term above the
// candidate's last) or a renewal (the same term again). (Nonce, ID) names
// it uniquely: Nonce is drawn at random per candidate boot, ID counts
// within the boot. A voter echoes both in its Reply, with the requester as
// To, and a candidate counts a grant only toward the request it answers:
// never one meant for the other controller, nor one from an earlier boot.
// Renewal marks a request sent by a leading candidate to renew its lease:
// a standby's voter notes it even when it refuses it, so the standby
// doesn't campaign under a live leader (Voter.LeaderSeen).
// Promise is the sender's own voter's running promise (Relay), relayed
// so the other voters hear it (#1029 layer 2).
type Request struct {
	From    NodeID
	Term    uint64
	Nonce   uint64
	ID      uint64
	Renewal bool
	Promise Relay
	// Seq numbers every request one boot sends, probes included, from 1
	// (#1029 3c): a voter takes a frame as new evidence only when its Seq
	// is above every Seq it heard from that boot, so a replayed or
	// duplicated old frame never counts as that boot still running.
	Seq uint64
	// Gen is the sender's committed adoption generation (VoterState.Gen; 0
	// from an uncommissioned controller): a voter that fenced the sender's
	// name refuses a request below its fence (#1029 3c-3c-1b).
	Gen uint64
	// Fences are the sending voter's fences (Voter.Fences): a voter that
	// lost its state learns them (Voter.LearnFences).
	Fences Fences
	// Uncommissioned: the sender has adopted no incarnation (a new
	// install, or a lost file). A fenced voter still hears its boot (Voter
	// handle, #1164 re-check): it may be a wiped old box, a lookalike of
	// the replacement.
	Uncommissioned bool
}

// Fences are a voter's fences on the pair's names (Voter.Fence), as its
// frames carry them (#1029 3c-3c-1b): a pair has two names, so two at
// most. A zero entry is none.
type Fences [2]FenceRec

// FenceRec is one name's fence: the lowest adoption generation of its
// state the voter takes.
type FenceRec struct {
	Name  NodeID
	Fence uint64
}

func (f Fences) put(b []byte) []byte {
	n := 0
	for _, r := range f {
		if r.Fence != 0 {
			n++
		}
	}
	b = append(b, byte(n))
	for _, r := range f {
		if r.Fence != 0 {
			b = putString(b, string(r.Name))
			b = binary.BigEndian.AppendUint64(b, r.Fence)
		}
	}
	return b
}

func (r *reader) fences() Fences {
	var f Fences
	n := int(r.byte1())
	if n > len(f) {
		r.bad = true
		return f
	}
	for i := 0; i < n && !r.bad; i++ {
		f[i] = FenceRec{Name: NodeID(r.str()), Fence: r.u64()}
		if f[i].Fence == 0 {
			r.bad = true
		}
	}
	return f
}

// Relay is a voter's running promise to ANOTHER node, as it relays it on
// every vote frame it sends (#1029 layer 2): the grantee, the boot nonce
// the promise was granted under, and how long it still runs on the
// sender's clock when the frame leaves. The zero Relay: no such promise.
// A voter relays only its own promise, never one it heard.
type Relay struct {
	To    NodeID
	Nonce uint64
	Left  time.Duration
	// Dup is a name this voter itself sees under two live boot nonces,
	// for DupLeft more (#1029 step 1): its hearers refuse new grants to it
	// too. "" when none.
	Dup     NodeID
	DupLeft time.Duration
}

// Reply answers one Request. Promised is the highest term the voter has
// promised, so a refused candidate learns what to ask for next.
// Incarnation is the witness's own incarnation (0 from a controller); a
// controller counts a witness grant only from the incarnation it accepted.
type Reply struct {
	From        NodeID
	To          NodeID // the candidate the request came from
	Term        uint64
	Nonce       uint64
	ID          uint64
	Granted     bool
	Promised    uint64
	Incarnation uint64
	Promise     Relay // the voter's running promise to another node (Relay)
	// Marked: this voter has confirmed To's name as run by two processes
	// (Voter.Marked), and gives it no new grant until an operator clears
	// the mark. A candidate told so stops campaigning (#1029 3c).
	Marked bool
	// ClearGen is To's name's clear generation at this voter
	// (Voter.DupGen): marked, the one a hold is set under; unmarked, a
	// clear's above it ends the hold from any boot of this voter
	// (Candidate.NoteMark, #1029 3c-3b).
	ClearGen uint64
	// VoterBoot identifies the voter's process boot (Voter.SetBoot): only a
	// later word from the same boot ends a hold that boot's mark set, since
	// a restarted voter has forgotten its marks (#1113 review).
	VoterBoot uint64
	// HeldBy names, for a marked name, the boot this voter holds the
	// incumbent: of the boots of To's name it heard, the one heard first
	// among those that sent a new frame within half a lease (Voter.HeldBy).
	// 0 when To's name is not marked or no boot is fresh. A copy that both
	// other voters name another boot of its name for a lease demotes itself
	// to a spare (Candidate.Spare, #1029 3c-2).
	HeldBy uint64
	// Gen is the voter's committed adoption generation (0 from the
	// witness): a candidate that fenced the voter's name counts no reply
	// below its fence (#1029 3c-3c-1b).
	Gen uint64
	// Fence, on a refusal for it: the requester's name's fence at this
	// voter (Voter.Fence, #1029 3c-3c-1b), the lowest generation of that
	// name's state it takes; the request's was below it: a state from
	// before an operator replaced the name (recommission --from-spare). 0
	// on every other reply.
	Fence uint64
	// Fences are the voter's fences (Request.Fences).
	Fences Fences
}

// AdoptKind is what an Adopt message says.
type AdoptKind uint8

const (
	// Propose asks the peer to adopt (Gen, Incarnation). Gen 0 is a query:
	// always refused, it asks for the peer's State.
	Propose AdoptKind = 1
	// Ack says the sender has durably adopted exactly (Gen, Incarnation),
	// in answer to the proposal of the same pair.
	Ack AdoptKind = 2
	// State says what the sender counts: (Gen, Incarnation) durably
	// adopted, (0, 0) when it is uncommissioned. It answers a proposal the
	// sender refuses, and a query.
	State AdoptKind = 3
)

func (k AdoptKind) String() string {
	switch k {
	case Propose:
		return "Propose"
	case Ack:
		return "Ack"
	case State:
		return "State"
	}
	return fmt.Sprintf("AdoptKind(%d)", uint8(k))
}

// Adopt is the witness-incarnation handshake between the two controllers,
// over the A–B link only (design §3.1, "The witness incarnation"). Gen is
// the adoption generation, which only rises, so a late, duplicated or
// earlier-boot message can't move a peer that has moved on. Floor is the
// sender's term floor: the highest term it knows was promised. Nonce names
// the sender's boot (its candidate's nonce); Echo is the nonce of the
// peer's boot it answers or addresses, so a message from before a
// receiver's restart (a lost file) can't commission it with a stale floor.
// Pending is the sender's proposal in flight, by generation (0: none).
// CommitGen and CommitInc are the sender's committed adoption (durably
// adopted; 0: uncommissioned): an uncommissioned receiver takes only a
// re-adoption of the incarnation the sender committed, never a switch
// (design §3.1, the two-of-three case). Operator is the floor of an
// operator's Recommission on a proposal of the incarnation it named (0:
// none); an uncommissioned receiver takes a proposal from a peer that is
// uncommissioned too only when it is set.
type Adopt struct {
	From        NodeID
	Kind        AdoptKind
	Gen         uint64
	Incarnation uint64
	Floor       uint64
	Nonce       uint64
	Echo        uint64
	Pending     uint64
	CommitGen   uint64
	CommitInc   uint64
	Operator    uint64
	// Fence is the sender's fence on the receiver's name (Voter.FenceOf),
	// 0 for none: a replacement re-adopting proposes at or above it
	// (#1029 3c-3c-1b).
	Fence uint64
}

// WireVersion is the vote protocol's frame version. A frame of another
// version is refused whole (ErrVersion). Version 2 added the relayed
// promise (#1029 layer 2), version 3 the duplicated name it carries
// (#1029 step 1), version 4 a request's Seq and a reply's Marked and
// VoterBoot (#1029 3c), version 5 a reply's HeldBy (#1029 3c-2), version 6
// a reply's ClearGen (#1029 3c-3b), version 7 a request's and a reply's
// Gen and a reply's Fence (#1029 3c-3c-1b): a pair
// that mixes releases across one can't vote, and says so
// (redundancy.version-mismatch) rather than failing in silence.
const WireVersion = 7

// FrameVersion returns the version byte a frame carries (0 when empty).
func FrameVersion(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

const (
	wireVersion = WireVersion
	kindRequest = 1
	kindReply   = 2
	kindAdopt   = 3
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Errors from decoding a message or a state file. A corrupt input is
// refused whole, never partly applied.
var (
	ErrCorrupt = errors.New("quorum: corrupt or truncated")
	ErrVersion = errors.New("quorum: unknown format version")
	ErrKind    = errors.New("quorum: unexpected message kind")
)

func putString(b []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	return append(append(b, byte(len(s))), s...)
}

func sealCRC(b []byte) []byte {
	return binary.BigEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// MarshalBinary encodes r: a version byte, a kind byte, the fields, and a
// CRC-32C over all of it.
func (r Request) MarshalBinary() ([]byte, error) {
	b := []byte{wireVersion, kindRequest}
	b = putString(b, string(r.From))
	b = binary.BigEndian.AppendUint64(b, r.Term)
	b = binary.BigEndian.AppendUint64(b, r.Nonce)
	b = binary.BigEndian.AppendUint64(b, r.ID)
	b = append(b, boolByte(r.Renewal))
	b = r.Promise.put(b)
	b = binary.BigEndian.AppendUint64(b, r.Seq)
	b = binary.BigEndian.AppendUint64(b, r.Gen)
	b = r.Fences.put(b)
	b = append(b, boolByte(r.Uncommissioned))
	return sealCRC(b), nil
}

func (p Relay) put(b []byte) []byte {
	b = putString(b, string(p.To))
	b = binary.BigEndian.AppendUint64(b, p.Nonce)
	b = binary.BigEndian.AppendUint64(b, uint64(max(p.Left, 0)))
	b = putString(b, string(p.Dup))
	return binary.BigEndian.AppendUint64(b, uint64(max(p.DupLeft, 0)))
}

// relay reads a Relay; a Left beyond int64 is corrupt.
func (r *reader) relay() Relay {
	p := Relay{To: NodeID(r.str()), Nonce: r.u64()}
	left := r.u64()
	if left > 1<<62 {
		r.bad = true
	}
	p.Left = time.Duration(left)
	p.Dup = NodeID(r.str())
	if dl := r.u64(); dl > 1<<62 {
		r.bad = true
	} else {
		p.DupLeft = time.Duration(dl)
	}
	return p
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// MarshalBinary encodes r like Request.MarshalBinary.
func (r Reply) MarshalBinary() ([]byte, error) {
	b := []byte{wireVersion, kindReply}
	b = putString(b, string(r.From))
	b = putString(b, string(r.To))
	b = binary.BigEndian.AppendUint64(b, r.Term)
	b = binary.BigEndian.AppendUint64(b, r.Nonce)
	b = binary.BigEndian.AppendUint64(b, r.ID)
	b = append(b, boolByte(r.Granted))
	b = binary.BigEndian.AppendUint64(b, r.Promised)
	b = binary.BigEndian.AppendUint64(b, r.Incarnation)
	b = r.Promise.put(b)
	b = append(b, boolByte(r.Marked))
	b = binary.BigEndian.AppendUint64(b, r.VoterBoot)
	b = binary.BigEndian.AppendUint64(b, r.HeldBy)
	b = binary.BigEndian.AppendUint64(b, r.ClearGen)
	b = binary.BigEndian.AppendUint64(b, r.Gen)
	b = binary.BigEndian.AppendUint64(b, r.Fence)
	b = r.Fences.put(b)
	return sealCRC(b), nil
}

// MarshalBinary encodes a like Request.MarshalBinary.
func (a Adopt) MarshalBinary() ([]byte, error) {
	b := []byte{wireVersion, kindAdopt}
	b = putString(b, string(a.From))
	b = append(b, byte(a.Kind))
	b = binary.BigEndian.AppendUint64(b, a.Gen)
	b = binary.BigEndian.AppendUint64(b, a.Incarnation)
	for _, v := range []uint64{a.Floor, a.Nonce, a.Echo, a.Pending, a.CommitGen, a.CommitInc, a.Operator, a.Fence} {
		b = binary.BigEndian.AppendUint64(b, v)
	}
	return sealCRC(b), nil
}

// reader walks a checked body; any short read marks it bad.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) byte1() byte {
	if len(r.b) < 1 {
		r.bad = true
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *reader) u64() uint64 {
	if len(r.b) < 8 {
		r.bad = true
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

// boolean reads a byte that must be 0 or 1.
func (r *reader) boolean() bool {
	switch r.byte1() {
	case 0:
		return false
	case 1:
		return true
	}
	r.bad = true
	return false
}

func (r *reader) str() string {
	n := int(r.byte1())
	if r.bad || len(r.b) < n {
		r.bad = true
		return ""
	}
	s := string(r.b[:n])
	r.b = r.b[n:]
	return s
}

// open checks the CRC and the version and returns the kind and the body.
func open(b []byte) (byte, *reader, error) {
	if len(b) < 2+4 {
		return 0, nil, ErrCorrupt
	}
	body, sum := b[:len(b)-4], binary.BigEndian.Uint32(b[len(b)-4:])
	if crc32.Checksum(body, castagnoli) != sum {
		return 0, nil, ErrCorrupt
	}
	if body[0] != wireVersion {
		return 0, nil, ErrVersion
	}
	return body[1], &reader{b: body[2:]}, nil
}

// UnmarshalBinary decodes a Request, refusing a corrupt, truncated or
// trailing-garbage frame, an unknown version, or a Reply.
func (r *Request) UnmarshalBinary(b []byte) error {
	kind, rd, err := open(b)
	if err != nil {
		return err
	}
	if kind != kindRequest {
		return ErrKind
	}
	v := Request{From: NodeID(rd.str()), Term: rd.u64(), Nonce: rd.u64(), ID: rd.u64()}
	v.Renewal = rd.boolean()
	v.Promise = rd.relay()
	v.Seq = rd.u64()
	v.Gen = rd.u64()
	v.Fences = rd.fences()
	v.Uncommissioned = rd.boolean()
	if rd.bad || len(rd.b) != 0 {
		return ErrCorrupt
	}
	*r = v
	return nil
}

// UnmarshalBinary decodes a Reply like Request.UnmarshalBinary.
func (r *Reply) UnmarshalBinary(b []byte) error {
	kind, rd, err := open(b)
	if err != nil {
		return err
	}
	if kind != kindReply {
		return ErrKind
	}
	v := Reply{From: NodeID(rd.str()), To: NodeID(rd.str()), Term: rd.u64(), Nonce: rd.u64(), ID: rd.u64()}
	v.Granted = rd.boolean()
	v.Promised, v.Incarnation = rd.u64(), rd.u64()
	v.Promise = rd.relay()
	v.Marked = rd.boolean()
	v.VoterBoot = rd.u64()
	v.HeldBy = rd.u64()
	v.ClearGen = rd.u64()
	v.Gen = rd.u64()
	v.Fence = rd.u64()
	v.Fences = rd.fences()
	if rd.bad || len(rd.b) != 0 {
		return ErrCorrupt
	}
	*r = v
	return nil
}

// UnmarshalBinary decodes an Adopt like Request.UnmarshalBinary, refusing
// an unknown kind too.
func (a *Adopt) UnmarshalBinary(b []byte) error {
	kind, rd, err := open(b)
	if err != nil {
		return err
	}
	if kind != kindAdopt {
		return ErrKind
	}
	v := Adopt{From: NodeID(rd.str()), Kind: AdoptKind(rd.byte1())}
	v.Gen, v.Incarnation, v.Floor = rd.u64(), rd.u64(), rd.u64()
	v.Nonce, v.Echo, v.Pending = rd.u64(), rd.u64(), rd.u64()
	v.CommitGen, v.CommitInc, v.Operator = rd.u64(), rd.u64(), rd.u64()
	v.Fence = rd.u64()
	if rd.bad || len(rd.b) != 0 || v.Kind < Propose || v.Kind > State {
		return ErrCorrupt
	}
	*a = v
	return nil
}
