//go:build !js

package camera

import (
	"errors"
)

// RTP (RFC 3550) and the H.264 payload format (RFC 6184), as far as a relay of one
// video track needs: packets are parsed with every length checked, and NAL units
// are put back together from single-NAL packets, STAP-A aggregates and FU-A
// fragments, into access units (pictures). A lost packet drops the picture it was
// in and the relay waits for the next keyframe; nothing a camera sends can make
// the depacketiser hold more than one access unit's bytes.

// RTPPacket is a parsed RTP packet; Payload aliases the bytes it was parsed from.
type RTPPacket struct {
	Marker  bool
	PT      uint8
	Seq     uint16
	TS      uint32
	Payload []byte
}

// ParseRTP reads an RTP packet header, refusing one that is not version 2 or is cut short.
func ParseRTP(b []byte) (RTPPacket, error) {
	if len(b) < 12 || b[0]>>6 != 2 {
		return RTPPacket{}, errors.New("rtp: not an RTP version 2 packet")
	}
	cc := int(b[0] & 0x0f)
	off := 12 + 4*cc
	if b[0]&0x10 != 0 { // header extension
		if len(b) < off+4 {
			return RTPPacket{}, errors.New("rtp: the header extension is cut short")
		}
		words := int(b[off+2])<<8 | int(b[off+3])
		off += 4 + 4*words
	}
	end := len(b)
	if b[0]&0x20 != 0 { // padding
		pad := int(b[len(b)-1])
		if pad == 0 || pad > len(b)-off {
			return RTPPacket{}, errors.New("rtp: the padding is wrong")
		}
		end -= pad
	}
	if off > end {
		return RTPPacket{}, errors.New("rtp: the header is longer than the packet")
	}
	return RTPPacket{
		Marker: b[1]&0x80 != 0, PT: b[1] & 0x7f, Seq: uint16(b[2])<<8 | uint16(b[3]),
		TS:      uint32(b[4])<<24 | uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7]),
		Payload: b[off:end],
	}, nil
}

// AccessUnit is one picture: its NAL units (without start codes or lengths), its
// RTP timestamp, and whether it is a keyframe (carries an IDR slice).
type AccessUnit struct {
	TS   uint32
	NALs [][]byte
	Key  bool
	Size int
}

const maxNALsPerAU = 1024

// Depacketizer rebuilds access units from RTP packets of one H.264 stream.
type Depacketizer struct {
	// SPS and PPS are the latest parameter sets seen in the stream (the SDP's
	// are the relay's first).
	SPS, PPS []byte

	au      AccessUnit
	have    bool
	fu      []byte // the FU-A NAL being put together
	fuOpen  bool
	seq     uint16
	seqOK   bool
	damaged bool // a packet was lost in this picture: it is dropped
	synced  bool // a whole keyframe has been seen since the start or the last loss
	Dropped int  // pictures dropped for loss or for being too large
}

// Push takes the next packet and calls emit for each access unit it completes.
func (d *Depacketizer) Push(p RTPPacket, emit func(AccessUnit)) error {
	if d.seqOK && p.Seq != d.seq+1 {
		// a gap (or a reorder, which a relay treats as loss): the picture in
		// progress and any fragment half-built are dropped
		d.loss()
	}
	d.seq, d.seqOK = p.Seq, true
	// a new timestamp ends the previous picture (a camera that sends no marker)
	if d.have && p.TS != d.au.TS {
		d.finish(emit)
	}
	if len(p.Payload) == 0 {
		return errors.New("rtp: an empty payload")
	}
	nt := nalType(p.Payload[0])
	switch {
	case nt >= 1 && nt <= 23:
		d.fuOpen, d.fu = false, d.fu[:0]
		d.addNAL(p.TS, p.Payload)
	case nt == nalSTAPA:
		d.fuOpen, d.fu = false, d.fu[:0]
		b := p.Payload[1:]
		for len(b) >= 2 {
			n := int(b[0])<<8 | int(b[1])
			b = b[2:]
			if n == 0 || n > len(b) {
				d.damaged = true
				return errors.New("rtp: a STAP-A unit runs past the packet")
			}
			d.addNAL(p.TS, b[:n])
			b = b[n:]
		}
	case nt == nalFUA:
		if len(p.Payload) < 3 {
			d.loss()
			return errors.New("rtp: an FU-A packet is cut short")
		}
		hdr, start, end := p.Payload[1], p.Payload[1]&0x80 != 0, p.Payload[1]&0x40 != 0
		if start {
			d.fu = append(d.fu[:0], (p.Payload[0]&0xe0)|(hdr&0x1f))
			d.fuOpen = true
		} else if !d.fuOpen {
			d.damaged = true // a fragment of a NAL whose start was lost
			break
		}
		if len(d.fu)+len(p.Payload)-2 > maxNALBytes {
			d.loss()
			d.Dropped++
			return errors.New("rtp: a NAL unit is larger than the limit")
		}
		d.fu = append(d.fu, p.Payload[2:]...)
		if end {
			nal := append([]byte(nil), d.fu...)
			d.fuOpen, d.fu = false, d.fu[:0]
			d.addNAL(p.TS, nal)
		}
	default:
		// types the relay does not carry (STAP-B, MTAP, FU-B): the picture is dropped
		d.damaged = true
	}
	if p.Marker {
		d.finish(emit)
	}
	return nil
}

// loss drops what was being built: the picture in progress, or (when the loss fell
// between pictures) the next one, which may have lost its first packets.
func (d *Depacketizer) loss() {
	d.fuOpen, d.fu = false, d.fu[:0]
	d.damaged = true
}

func (d *Depacketizer) addNAL(ts uint32, nal []byte) {
	if len(nal) == 0 {
		return
	}
	if !d.have {
		d.au = AccessUnit{TS: ts}
		d.have = true
	}
	if len(nal) > maxNALBytes || d.au.Size+len(nal) > maxAUBytes || len(d.au.NALs) >= maxNALsPerAU {
		d.damaged = true
		return
	}
	switch nalType(nal[0]) {
	case nalSPS:
		d.SPS = append([]byte(nil), nal...)
	case nalPPS:
		d.PPS = append([]byte(nil), nal...)
	case nalIDR:
		d.au.Key = true
	}
	d.au.NALs = append(d.au.NALs, append([]byte(nil), nal...))
	d.au.Size += len(nal)
}

func (d *Depacketizer) finish(emit func(AccessUnit)) {
	if !d.have {
		return
	}
	au, bad := d.au, d.damaged
	d.au, d.have, d.damaged = AccessUnit{}, false, false
	if bad {
		// a lost packet: this picture is dropped, and so is everything after it
		// until a whole keyframe, which the pictures after it were coded against
		d.Dropped++
		d.synced = false
		return
	}
	if !d.synced {
		if !au.Key || !au.startsCleanly() {
			d.Dropped++
			return
		}
		d.synced = true
	}
	emit(au)
}
