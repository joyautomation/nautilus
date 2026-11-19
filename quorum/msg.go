package quorum

import (
	"encoding/binary"
	"errors"
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
type Request struct {
	From  NodeID
	Term  uint64
	Nonce uint64
	ID    uint64
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

const (
	wireVersion = 1
	kindRequest = 1
	kindReply   = 2
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
	return sealCRC(b), nil
}

// MarshalBinary encodes r like Request.MarshalBinary.
func (r Reply) MarshalBinary() ([]byte, error) {
	b := []byte{wireVersion, kindReply}
	b = putString(b, string(r.From))
	b = putString(b, string(r.To))
	b = binary.BigEndian.AppendUint64(b, r.Term)
	b = binary.BigEndian.AppendUint64(b, r.Nonce)
	b = binary.BigEndian.AppendUint64(b, r.ID)
	g := byte(0)
	if r.Granted {
		g = 1
	}
	b = append(b, g)
	b = binary.BigEndian.AppendUint64(b, r.Promised)
	b = binary.BigEndian.AppendUint64(b, r.Incarnation)
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
	switch rd.byte1() {
	case 0:
	case 1:
		v.Granted = true
	default:
		rd.bad = true
	}
	v.Promised, v.Incarnation = rd.u64(), rd.u64()
	if rd.bad || len(rd.b) != 0 {
		return ErrCorrupt
	}
	*r = v
	return nil
}
