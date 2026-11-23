//go:build !js

package camera

import (
	"bytes"
	"testing"
)

func collect(t *testing.T, pkts []RTPPacket) (*Depacketizer, []AccessUnit) {
	t.Helper()
	d := &Depacketizer{}
	var got []AccessUnit
	for _, p := range pkts {
		d.Push(p, func(a AccessUnit) { got = append(got, a) })
	}
	return d, got
}

func sameAUs(t *testing.T, got, want []AccessUnit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d pictures, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].TS != want[i].TS || got[i].Key != want[i].Key || len(got[i].NALs) != len(want[i].NALs) {
			t.Fatalf("picture %d: ts %d key %v %d NALs, want ts %d key %v %d NALs", i, got[i].TS, got[i].Key, len(got[i].NALs), want[i].TS, want[i].Key, len(want[i].NALs))
		}
		for j := range want[i].NALs {
			if !bytes.Equal(got[i].NALs[j], want[i].NALs[j]) {
				t.Fatalf("picture %d NAL %d differs", i, j)
			}
		}
	}
}

func TestThePacketsOfARealClipBecomeItsPicturesAgainAtAnyPacketSize(t *testing.T) {
	want := clipAUs(t)
	for _, mtu := range []int{60, 100, 400, 1200, 1400} {
		pkts := clipPackets(want, mtu)
		var raw []RTPPacket
		for _, p := range pkts { // through the wire format
			b := append([]byte{0x80, p.PT, byte(p.Seq >> 8), byte(p.Seq), byte(p.TS >> 24), byte(p.TS >> 16), byte(p.TS >> 8), byte(p.TS), 1, 2, 3, 4}, p.Payload...)
			if p.Marker {
				b[1] |= 0x80
			}
			q, err := ParseRTP(b)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, q)
		}
		d, got := collect(t, raw)
		sameAUs(t, got, want)
		if d.Dropped != 0 || len(d.SPS) == 0 || len(d.PPS) == 0 {
			t.Errorf("mtu %d: dropped %d, SPS %d bytes, PPS %d bytes", mtu, d.Dropped, len(d.SPS), len(d.PPS))
		}
	}
}

func TestALostPacketDropsItsPictureAndNoOther(t *testing.T) {
	want := clipAUs(t)
	pkts := clipPackets(want, 100)
	// drop a packet from the middle of the second keyframe's slices
	var drop int
	var in int
	for i, p := range pkts {
		if p.TS == want[10].TS {
			in++
			if in == 3 {
				drop = i
			}
		}
	}
	lossy := append(append([]RTPPacket{}, pkts[:drop]...), pkts[drop+1:]...)
	d, got := collect(t, lossy)
	if len(got) != len(want)-1 || d.Dropped != 1 {
		t.Fatalf("%d pictures out, %d dropped: want %d and 1", len(got), d.Dropped, len(want)-1)
	}
	for _, au := range got {
		if au.TS == want[10].TS {
			t.Error("the damaged picture was emitted")
		}
	}
	// a whole picture lost between pictures: the next is dropped too (it may
	// have lost its first packets), the rest are whole
	var lossy2 []RTPPacket
	for _, p := range pkts {
		if p.TS != want[4].TS {
			lossy2 = append(lossy2, p)
		}
	}
	_, got2 := collect(t, lossy2)
	if len(got2) < len(want)-2 || len(got2) > len(want)-1 {
		t.Errorf("a lost picture left %d of %d", len(got2), len(want))
	}
}

func TestFragmentsOfANALWhoseStartWasLostAreNeverAssembled(t *testing.T) {
	want := clipAUs(t)
	pkts := clipPackets(want, 60)
	// remove the FU-A start of the first keyframe's slice
	for i, p := range pkts {
		if len(p.Payload) > 1 && nalType(p.Payload[0]) == nalFUA && p.Payload[1]&0x80 != 0 {
			pkts = append(pkts[:i], pkts[i+1:]...)
			break
		}
	}
	_, got := collect(t, pkts)
	if len(got) != len(want)-1 {
		t.Errorf("%d pictures out, want %d (the first, damaged, is dropped)", len(got), len(want)-1)
	}
}

func TestAStreamWithoutMarkersStillEndsPicturesByTimestamp(t *testing.T) {
	want := clipAUs(t)
	pkts := clipPackets(want, 1400)
	for i := range pkts {
		pkts[i].Marker = false
	}
	_, got := collect(t, pkts)
	// the last picture has no later timestamp to end it
	if len(got) != len(want)-1 {
		t.Errorf("%d pictures from a stream with no markers, want %d", len(got), len(want)-1)
	}
}

func TestHostilePacketsAreBoundedAndRefused(t *testing.T) {
	d := &Depacketizer{}
	emit := func(AccessUnit) {}
	// an FU-A that never ends: the buffer stops at the limit
	d.Push(RTPPacket{Seq: 1, TS: 1, Payload: []byte{nalFUA | 0x60, 0x80 | nalIDR, 1, 2, 3}}, emit)
	big := make([]byte, 60000)
	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = d.Push(RTPPacket{Seq: uint16(2 + i), TS: 1, Payload: append([]byte{nalFUA | 0x60, nalIDR}, big...)}, emit)
	}
	if err == nil || len(d.fu) > maxNALBytes {
		t.Errorf("an endless FU-A: %v, buffer %d", err, len(d.fu))
	}
	// an STAP-A whose lengths run past the packet
	d = &Depacketizer{}
	if err := d.Push(RTPPacket{Seq: 1, TS: 1, Marker: true, Payload: []byte{nalSTAPA, 0xff, 0xff, 1, 2}}, emit); err == nil {
		t.Error("an STAP-A running past its packet was accepted")
	}
	// too many NAL units in one picture, and one picture too large
	d = &Depacketizer{}
	var got int
	for i := 0; i < maxNALsPerAU+50; i++ {
		d.Push(RTPPacket{Seq: uint16(i + 1), TS: 7, Payload: []byte{nalSlice | 0x60, 1}}, func(AccessUnit) { got++ })
	}
	d.Push(RTPPacket{Seq: uint16(maxNALsPerAU + 51), TS: 7, Marker: true, Payload: []byte{nalSlice | 0x60, 1}}, func(AccessUnit) { got++ })
	if got != 0 || d.Dropped != 1 {
		t.Errorf("a picture of %d NAL units: %d emitted, %d dropped", maxNALsPerAU+51, got, d.Dropped)
	}
	// short and unsupported payloads are errors or drops, never panics
	d = &Depacketizer{}
	for _, p := range [][]byte{{}, {nalFUA}, {nalFUA, 0x80}, {29, 1, 2, 3}, {30}, {nalSTAPA}, {0}, {0x1f, 0, 0}} {
		d.Push(RTPPacket{Seq: 1, TS: 1, Payload: p}, emit)
	}
	for _, b := range [][]byte{nil, {0x80}, make([]byte, 11), {0x40, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, append([]byte{0x90, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, 0, 0), append([]byte{0xa0, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, 0, 0, 0, 200)} {
		if _, err := ParseRTP(b); err == nil {
			t.Errorf("ParseRTP(% x) was accepted", b)
		}
	}
}
