package quorum

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

// Mark is how far a standby is in sync (design §3.3): the (Term, Scan) of
// the last state image it acknowledged, and whether that was current.
// The zero Mark is unknown, and an unknown mark counts as out of sync. It
// is carried and persisted from the first format so PR 4 need not change
// the file; nothing in this package reads it yet.
type Mark struct {
	Known   bool
	Current bool
	Term    uint64
	Scan    uint64
}

// VoterState is what a voter persists, and fsyncs, before it answers.
// promiseUntil is NOT here: it is a reading of this boot's clock, and a
// restarted voter covers it by granting nothing for one lease instead.
type VoterState struct {
	// Promised is the highest term this voter has granted. It only rises.
	Promised uint64
	// GrantedTo is the candidate Promised was granted to.
	GrantedTo NodeID
	// Incarnation is, on the witness, its own incarnation, drawn at first
	// start; on a controller, the witness incarnation it last adopted. A
	// controller with none (0) is uncommissioned: a new install, or a lost
	// file. Its voter grants nothing until it adopts one (Adoption).
	Incarnation uint64
	// InSync is the last in-sync mark recorded (§3.3).
	InSync Mark

	// The rest is a controller's handshake state (Adoption, design §3.1).
	// Gen is the adoption generation of Incarnation; it only rises.
	Gen uint64
	// PendingGen, PendingInc are a proposal in flight (PendingGen 0: none).
	// While one is, the controller counts no witness at all: it stopped
	// counting the old incarnation before it asked (forward only).
	PendingGen, PendingInc uint64
	// Floor: this voter grants no term at or below it. Raised at every
	// adoption to the highest term either controller (and, for a lost
	// file, the witness) knew was promised, so no term is granted twice.
	Floor uint64

	// Lease is the longest lease a promise of this voter may still be
	// running under. A voter restarted with a shorter lease waits this one
	// out before it grants (NewVoter): its last promise was made under it
	// (#1021 review). It drops to the voter's own lease at its first grant
	// after that wait, written with the grant.
	Lease time.Duration
}

const stateMagic = "nqv"

// MarshalBinary encodes s for its state file: a magic, a version byte, a
// length, the fields, and a CRC-32C, so a torn write is detected.
func (s VoterState) MarshalBinary() ([]byte, error) {
	var p []byte
	p = binary.BigEndian.AppendUint64(p, s.Promised)
	p = putString(p, string(s.GrantedTo))
	p = binary.BigEndian.AppendUint64(p, s.Incarnation)
	flags := byte(0)
	if s.InSync.Known {
		flags |= 1
	}
	if s.InSync.Current {
		flags |= 2
	}
	p = append(p, flags)
	p = binary.BigEndian.AppendUint64(p, s.InSync.Term)
	p = binary.BigEndian.AppendUint64(p, s.InSync.Scan)
	for _, v := range []uint64{s.Gen, s.PendingGen, s.PendingInc, s.Floor, uint64(s.Lease)} {
		p = binary.BigEndian.AppendUint64(p, v)
	}
	b := append([]byte(stateMagic), wireVersion)
	b = binary.BigEndian.AppendUint32(b, uint32(len(p)))
	b = append(b, p...)
	return binary.BigEndian.AppendUint32(b, crc32.Checksum(b, castagnoli)), nil
}

// UnmarshalBinary decodes a state file. Any damage (a torn tail, a flipped
// bit, a foreign file) is ErrCorrupt and leaves s unchanged: the caller
// starts the voter with no state, which waits one lease (NewVoter).
func (s *VoterState) UnmarshalBinary(b []byte) error {
	const head = len(stateMagic) + 1 + 4
	if len(b) < head+4 || string(b[:len(stateMagic)]) != stateMagic {
		return ErrCorrupt
	}
	n := int(binary.BigEndian.Uint32(b[len(stateMagic)+1:]))
	if len(b) != head+n+4 {
		return ErrCorrupt
	}
	body := b[:head+n]
	if crc32.Checksum(body, castagnoli) != binary.BigEndian.Uint32(b[head+n:]) {
		return ErrCorrupt
	}
	if b[len(stateMagic)] != wireVersion {
		return ErrVersion
	}
	rd := &reader{b: b[head : head+n]}
	v := VoterState{Promised: rd.u64(), GrantedTo: NodeID(rd.str()), Incarnation: rd.u64()}
	flags := rd.byte1()
	v.InSync = Mark{Known: flags&1 != 0, Current: flags&2 != 0, Term: rd.u64(), Scan: rd.u64()}
	v.Gen, v.PendingGen, v.PendingInc, v.Floor = rd.u64(), rd.u64(), rd.u64(), rd.u64()
	v.Lease = time.Duration(rd.u64())
	if rd.bad || len(rd.b) != 0 || flags&^3 != 0 || v.Lease < 0 {
		return ErrCorrupt
	}
	*s = v
	return nil
}
