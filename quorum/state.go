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

	// Owner and Cluster name who wrote the file: the member (a, b, or the
	// witness) and the pair's cluster ID. The driver stamps them on every
	// write and refuses to start over a file another member or another
	// pair wrote, so a state file carried to the wrong box (an env file
	// copied with NAUTILUS_NODE in it) never votes under a name it did not
	// promise as (#1028 review). Empty in a file never written.
	Owner   NodeID
	Cluster string
	// Box is the fingerprint of the box that wrote the file (the driver's
	// node.Options.Box: its machine-id and the MACs of the interfaces that
	// carry the pair's links). A controller whose box differs treats the
	// file as lost: a cloned disk must not vote as the original (#1029).
	// Empty on the witness, and in a file never written.
	Box string
	// GrantNonce is the boot nonce Promised was granted to GrantedTo under:
	// a term goes to one boot of a name, also across this voter's restart
	// (#1029 step 1, #1094 review). 0 in a file before format 5.
	GrantNonce uint64
	// Spare: this controller demoted itself to a spare, a copy of a member
	// another boot of its name holds (Candidate.Spare, #1029 3c-2). Written
	// before it goes quiet, and kept by every later write: a spare stays
	// one across restarts and a box change, until an operator turns it
	// back. Only a spare's file is written in format 6.
	Spare bool
	// Dups are the voter's duplicate records (#1029 3c-3b): the names it
	// marks, the boots an operator's clear retired, the clear
	// generations. Only a file with a record is written in format 7.
	Dups []DupRecord
	// RecordsFrom and Epochs are the voter's epoch records (#710 S2c): the
	// highest epoch it granted each name in a campaign, written with the
	// grant, so a hot takeover can tell no leadership came after its
	// image. The records cover every grant from RecordsFrom on; 0 is none
	// kept yet. Only a file with records is written in format 9.
	RecordsFrom uint64
	Epochs      []EpochRecord
}

// EpochRecord is the highest epoch a voter granted one name in a
// campaign (VoterState.Epochs).
type EpochRecord struct {
	Name  NodeID
	Epoch uint64
}

const stateMagic = "nqv"

// stateVersion is the state file's format: 2 since VoterState.Lease, 3
// since Owner and Cluster, 4 since Box, 5 since GrantNonce, 6 since Spare,
// 7 since Dups, 8 since DupRecord.Fence, 9 since the epoch records. Only
// a file with epoch records is written as 9, only one with a fence as 8, only one with a duplicate record is written as 7, and
// only a spare's without one as 6; every other is still 5, so a member
// with neither can go back to an earlier release. A version-4
// file still reads (GrantNonce 0, as before it was kept), so an upgrade
// keeps the voter's promises; a file of any other version reads as
// ErrVersion, not as corrupt.
const (
	stateVersion      = 9
	stateVersionFence = 8 // a file with a fence but no epoch records
	stateVersionDups  = 7 // a file with duplicate records but no fence
	stateVersionSpare = 6 // a spare's file with no duplicate record
	stateVersionPlain = 5 // what a file without the spare flag is written as
)

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
	p = putString(p, string(s.Owner))
	p = putString(p, s.Cluster)
	p = putString(p, s.Box)
	p = binary.BigEndian.AppendUint64(p, s.GrantNonce)
	version := byte(stateVersionPlain)
	epochs := s.RecordsFrom != 0
	if s.Spare || len(s.Dups) > 0 || epochs || mut("state_always_v6") {
		version = stateVersionSpare
		p = append(p, boolByte(s.Spare))
	}
	if len(s.Dups) > 0 || epochs {
		version = stateVersionDups
		for _, r := range s.Dups {
			if r.Fence != 0 {
				version = stateVersionFence
			}
		}
		if epochs {
			version = stateVersion
		}
		p = append(p, byte(len(s.Dups)))
		for _, r := range s.Dups {
			p = putString(p, string(r.Name))
			p = append(p, boolByte(r.Marked))
			p = binary.BigEndian.AppendUint64(p, r.Gen)
			p = putBoots(p, r.Boots)
			p = putBoots(p, r.Retired)
			if version >= stateVersionFence {
				p = binary.BigEndian.AppendUint64(p, r.Fence)
			}
		}
	}
	if epochs {
		p = binary.BigEndian.AppendUint64(p, s.RecordsFrom)
		p = append(p, byte(len(s.Epochs)))
		for _, r := range s.Epochs {
			p = putString(p, string(r.Name))
			p = binary.BigEndian.AppendUint64(p, r.Epoch)
		}
	}
	b := append([]byte(stateMagic), version)
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
	version := b[len(stateMagic)]
	if version < 4 || version > stateVersion {
		return ErrVersion
	}
	rd := &reader{b: b[head : head+n]}
	v := VoterState{Promised: rd.u64(), GrantedTo: NodeID(rd.str()), Incarnation: rd.u64()}
	flags := rd.byte1()
	v.InSync = Mark{Known: flags&1 != 0, Current: flags&2 != 0, Term: rd.u64(), Scan: rd.u64()}
	v.Gen, v.PendingGen, v.PendingInc, v.Floor = rd.u64(), rd.u64(), rd.u64(), rd.u64()
	v.Lease = time.Duration(rd.u64())
	v.Owner, v.Cluster, v.Box = NodeID(rd.str()), rd.str(), rd.str()
	if version >= 5 {
		v.GrantNonce = rd.u64()
	}
	if version >= 6 {
		v.Spare = rd.boolean()
	}
	if version >= 7 {
		k := int(rd.byte1())
		if (k == 0 && version < stateVersion) || k > maxDupRecords {
			rd.bad = true
		}
		for i := 0; i < k && !rd.bad; i++ {
			r := DupRecord{Name: NodeID(rd.str()), Marked: rd.boolean(), Gen: rd.u64()}
			r.Boots, r.Retired = rd.boots(), rd.boots()
			if version >= stateVersionFence {
				r.Fence = rd.u64()
			}
			v.Dups = append(v.Dups, r)
		}
	}
	if version >= stateVersion {
		v.RecordsFrom = rd.u64()
		k := int(rd.byte1())
		if v.RecordsFrom == 0 || k > maxEpochRecords {
			rd.bad = true
		}
		for i := 0; i < k && !rd.bad; i++ {
			r := EpochRecord{Name: NodeID(rd.str()), Epoch: rd.u64()}
			if r.Name == "" || r.Epoch == 0 {
				rd.bad = true
			}
			v.Epochs = append(v.Epochs, r)
		}
	}
	if rd.bad || len(rd.b) != 0 || flags&^3 != 0 || v.Lease < 0 {
		return ErrCorrupt
	}
	*s = v
	return nil
}

// putBoots appends a list of boots: a count byte, then each nonce and Seq.
func putBoots(p []byte, bs []BootSeq) []byte {
	p = append(p, byte(len(bs)))
	for _, b := range bs {
		p = binary.BigEndian.AppendUint64(p, b.Nonce)
		p = binary.BigEndian.AppendUint64(p, b.Seq)
	}
	return p
}

// boots reads a list putBoots wrote, of at most maxRetired.
func (r *reader) boots() []BootSeq {
	n := int(r.byte1())
	if n > maxRetired {
		r.bad = true
		return nil
	}
	var out []BootSeq
	for i := 0; i < n && !r.bad; i++ {
		out = append(out, BootSeq{Nonce: r.u64(), Seq: r.u64()})
	}
	return out
}
