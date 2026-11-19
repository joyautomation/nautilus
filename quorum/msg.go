package quorum

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
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
type Request struct {
	From    NodeID
	Term    uint64
	Nonce   uint64
	ID      uint64
	Renewal bool
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
// sender's term floor: the highest term it knows was promised.
type Adopt struct {
	From        NodeID
	Kind        AdoptKind
	Gen         uint64
	Incarnation uint64
	Floor       uint64
}

const (
	wireVersion = 1
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
	return sealCRC(b), nil
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
	return sealCRC(b), nil
}

// MarshalBinary encodes a like Request.MarshalBinary.
func (a Adopt) MarshalBinary() ([]byte, error) {
	b := []byte{wireVersion, kindAdopt}
	b = putString(b, string(a.From))
	b = append(b, byte(a.Kind))
	b = binary.BigEndian.AppendUint64(b, a.Gen)
	b = binary.BigEndian.AppendUint64(b, a.Incarnation)
	b = binary.BigEndian.AppendUint64(b, a.Floor)
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
	if rd.bad || len(rd.b) != 0 || v.Kind < Propose || v.Kind > State {
		return ErrCorrupt
	}
	*a = v
	return nil
}
