package quorum

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestMessageRoundTrip(t *testing.T) {
	rq := Request{From: "A", Term: 7, Nonce: 0xA1, ID: 99, Renewal: true, Promise: Relay{To: "B", Nonce: 0xB2, Left: 1500 * time.Millisecond, Dup: "A", DupLeft: 900 * time.Millisecond}, Seq: 1 << 40}
	b, _ := rq.MarshalBinary()
	var got Request
	if err := got.UnmarshalBinary(b); err != nil || got != rq {
		t.Fatalf("request: %+v %v", got, err)
	}
	rp := Reply{From: "W", To: "A", Term: 7, Nonce: 0xA1, ID: 99, Granted: true, Promised: 7, Incarnation: 42, Promise: Relay{To: "A", Nonce: 0xA0, Left: time.Second}, Marked: true, VoterBoot: 0xB007}
	b, _ = rp.MarshalBinary()
	var gr Reply
	if err := gr.UnmarshalBinary(b); err != nil || gr != rp {
		t.Fatalf("reply: %+v %v", gr, err)
	}
	var wrong Request
	if err := wrong.UnmarshalBinary(b); !errors.Is(err, ErrKind) {
		t.Fatalf("a reply decoded as a request: %v", err)
	}
}

// Every truncation and every single-bit flip of a frame is refused whole
// (F31, the vote half).
func TestMessageCorruptionRefused(t *testing.T) {
	b, _ := Reply{From: "W", To: "A", Term: 7, Nonce: 0xA1, ID: 99, Granted: true, Promised: 7, Incarnation: 42, Promise: Relay{To: "B", Nonce: 3, Left: 9}}.MarshalBinary()
	for n := 0; n < len(b); n++ {
		var r Reply
		if err := r.UnmarshalBinary(b[:n]); err == nil {
			t.Fatalf("a frame truncated to %d bytes decoded", n)
		}
	}
	for i := range b {
		for bit := 0; bit < 8; bit++ {
			c := append([]byte(nil), b...)
			c[i] ^= 1 << bit
			var r Reply
			if err := r.UnmarshalBinary(c); err == nil {
				t.Fatalf("bit %d of byte %d flipped and the frame decoded", bit, i)
			}
		}
	}
	// Trailing garbage, re-sealed with a good CRC, is still refused.
	body := append(append([]byte(nil), b[:len(b)-4]...), 0)
	var r Reply
	if err := r.UnmarshalBinary(sealCRC(body)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("trailing byte: %v", err)
	}
	// An unknown version with a good CRC is ErrVersion.
	v := append([]byte(nil), b[:len(b)-4]...)
	v[0] = 9
	if err := r.UnmarshalBinary(sealCRC(v)); !errors.Is(err, ErrVersion) {
		t.Fatalf("version 9: %v", err)
	}
}

// A state file torn at EVERY byte offset is detected, and leaves the
// target untouched (F26, the T1 half).
func TestStateFileTornAtEveryOffset(t *testing.T) {
	s := VoterState{Promised: 12, GrantedTo: "B", Incarnation: 42,
		InSync: Mark{Known: true, Current: true, Term: 12, Scan: 3400}, Owner: "a", Cluster: "0123456789abcdef0123456789abcdef", Box: "m=1 n=2"}
	b, _ := s.MarshalBinary()
	var got VoterState
	if err := got.UnmarshalBinary(b); err != nil || got != s {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for n := 0; n < len(b); n++ {
		keep := VoterState{Promised: 1}
		if err := keep.UnmarshalBinary(b[:n]); err == nil {
			t.Fatalf("a file torn at byte %d decoded", n)
		}
		if keep != (VoterState{Promised: 1}) {
			t.Fatalf("a torn file at byte %d changed the target: %+v", n, keep)
		}
	}
	for i := range b {
		c := append([]byte(nil), b...)
		c[i] ^= 0x10
		var x VoterState
		if err := x.UnmarshalBinary(c); err == nil {
			t.Fatalf("byte %d flipped and the file decoded", i)
		}
	}
}

// A state file of another format version (1: before VoterState.Lease; 2:
// before Owner and Cluster; 3: before Box) reads as ErrVersion, not as
// corrupt. A version-4 file (before GrantNonce) still reads, with
// GrantNonce 0, so an upgrade keeps the voter's promises.
func TestStateFileVersion(t *testing.T) {
	st5 := VoterState{Promised: 3, GrantedTo: "a", Incarnation: 9, Lease: lease, Owner: "w", Cluster: "c", GrantNonce: 0xAB}
	b, _ := st5.MarshalBinary()
	if b[len(stateMagic)] != 5 {
		t.Fatalf("state version %d, want 5", b[len(stateMagic)])
	}
	var back VoterState
	if err := back.UnmarshalBinary(b); err != nil || back != st5 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	// Version 4: the same file without the trailing GrantNonce.
	v4 := append([]byte(nil), b[:len(b)-4-8]...)
	v4[len(stateMagic)] = 4
	binary.BigEndian.PutUint32(v4[len(stateMagic)+1:], uint32(len(v4)-len(stateMagic)-1-4))
	var old4 VoterState
	want := st5
	want.GrantNonce = 0
	if err := old4.UnmarshalBinary(sealCRC(v4)); err != nil || old4 != want {
		t.Fatalf("a version-4 file: %+v %v, want %+v", old4, err, want)
	}
	for _, v := range []byte{1, 2, 3} {
		old := append([]byte(nil), b[:len(b)-4]...)
		old[len(stateMagic)] = v
		var st VoterState
		if err := st.UnmarshalBinary(sealCRC(old)); !errors.Is(err, ErrVersion) {
			t.Fatalf("a version-%d file: %v, want ErrVersion", v, err)
		}
	}
}
