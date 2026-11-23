//go:build !js

package camera

import (
	"encoding/binary"
	"errors"
)

// Fragmented MP4 (ISO 14496-12, the CMAF subset HLS and MSE take) for one H.264
// track: an init segment (ftyp + moov) made from the SPS and PPS, and media
// fragments (moof + mdat) of whole pictures. Nothing here decodes video: the NAL
// units go in as they came from the camera, length-prefixed.

// VideoTimescale is the track's timescale: RTP's 90 kHz clock, so timestamps are
// copied unchanged.
const VideoTimescale = 90000

// Sample is one picture in a fragment.
type Sample struct {
	Data     []byte // the NAL units, each preceded by its 4-byte length
	Duration uint32 // in VideoTimescale ticks
	Key      bool
}

// AVCC turns an access unit's NAL units into a sample's data. Parameter sets and
// access unit delimiters are left out: they are in the init segment.
func AVCC(nals [][]byte) []byte {
	// sized over the NAL units kept: a buffer sized over all of them would keep the
	// capacity of the ones left out (a hostile camera's megabyte delimiters)
	n := 0
	for _, x := range nals {
		switch nalType(x[0]) {
		case nalSPS, nalPPS, nalAUD:
			continue
		}
		n += 4 + len(x)
	}
	out := make([]byte, 0, n)
	for _, x := range nals {
		switch nalType(x[0]) {
		case nalSPS, nalPPS, nalAUD:
			continue
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(x)))
		out = append(out, x...)
	}
	return out
}

func box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 0, n)
	b = binary.BigEndian.AppendUint32(b, uint32(n))
	b = append(b, typ...)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// fullbox is a box with a version and flags.
func fullbox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	return box(typ, append([][]byte{{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}}, payload...)...)
}

var unityMatrix = []byte{
	0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0,
}

// InitSegment is the ftyp and moov of a track of the picture size in info, with
// sps and pps (NAL units with their header byte) in its avcC.
func InitSegment(sps, pps []byte, info SPSInfo) ([]byte, error) {
	if len(sps) < 4 || len(pps) < 1 || len(sps) > 4096 || len(pps) > 4096 {
		return nil, errors.New("mp4: the parameter sets are missing or too long")
	}
	avcc := box("avcC",
		[]byte{1, sps[1], sps[2], sps[3], 0xff, 0xe1}, u16(uint16(len(sps))), sps,
		[]byte{1}, u16(uint16(len(pps))), pps)
	avc1 := box("avc1",
		make([]byte, 6), u16(1), // reserved, data reference index
		make([]byte, 16), // pre_defined, reserved
		u16(uint16(info.Width)), u16(uint16(info.Height)),
		u32(0x00480000), u32(0x00480000), // 72 dpi
		u32(0), u16(1), // reserved, frame count
		make([]byte, 32),                // compressor name
		u16(0x0018), []byte{0xff, 0xff}, // depth, pre_defined -1
		avcc)
	stsd := fullbox("stsd", 0, 0, u32(1), avc1)
	empty := func(typ string, extra ...[]byte) []byte {
		return fullbox(typ, 0, 0, append([][]byte{u32(0)}, extra...)...)
	}
	stbl := box("stbl", stsd, empty("stts"), empty("stsc"), fullbox("stsz", 0, 0, u32(0), u32(0)), empty("stco"))
	dinf := box("dinf", fullbox("dref", 0, 0, u32(1), fullbox("url ", 0, 1)))
	minf := box("minf", fullbox("vmhd", 0, 1, make([]byte, 8)), dinf, stbl)
	hdlr := fullbox("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 12), []byte("video\x00"))
	mdhd := fullbox("mdhd", 0, 0, u32(0), u32(0), u32(VideoTimescale), u32(0), u16(0x55c4), u16(0))
	mdia := box("mdia", mdhd, hdlr, minf)
	tkhd := fullbox("tkhd", 0, 7, u32(0), u32(0), u32(1), u32(0), u32(0), make([]byte, 8), u16(0), u16(0), u16(0), u16(0),
		unityMatrix, u32(uint32(info.Width)<<16), u32(uint32(info.Height)<<16))
	trak := box("trak", tkhd, mdia)
	mvhd := fullbox("mvhd", 0, 0, u32(0), u32(0), u32(VideoTimescale), u32(0), u32(0x00010000), u16(0x0100), make([]byte, 10),
		unityMatrix, make([]byte, 24), u32(2))
	trex := fullbox("trex", 0, 0, u32(1), u32(1), u32(0), u32(0), u32(0x01010000))
	moov := box("moov", mvhd, trak, box("mvex", trex))
	ftyp := box("ftyp", []byte("isom"), u32(0x200), []byte("isomiso5iso6avc1mp41"))
	return append(ftyp, moov...), nil
}

const (
	sampleKey    = 0x02000000
	sampleNonKey = 0x01010000
)

// Fragment is a moof and an mdat holding samples, the first at decode time
// baseTime. seq numbers the fragment from 1.
func Fragment(seq uint32, baseTime uint64, samples []Sample) []byte {
	dataLen := 0
	trun := make([]byte, 0, 8+12*len(samples))
	trun = binary.BigEndian.AppendUint32(trun, uint32(len(samples)))
	trun = binary.BigEndian.AppendUint32(trun, 0) // data offset: filled in below
	for _, s := range samples {
		fl := uint32(sampleNonKey)
		if s.Key {
			fl = sampleKey
		}
		trun = binary.BigEndian.AppendUint32(trun, s.Duration)
		trun = binary.BigEndian.AppendUint32(trun, uint32(len(s.Data)))
		trun = binary.BigEndian.AppendUint32(trun, fl)
		dataLen += len(s.Data)
	}
	// moof = mfhd + traf(tfhd + tfdt + trun); the data offset is moof's size + 8
	// (the mdat header), because tfhd says the base is the moof
	build := func(off uint32) []byte {
		t := append([]byte(nil), trun...)
		binary.BigEndian.PutUint32(t[4:], off)
		tfhd := fullbox("tfhd", 0, 0x020000, u32(1))
		tfdt := fullbox("tfdt", 1, 0, u64(baseTime))
		trunBox := fullbox("trun", 0, 0x000701, t)
		return box("moof", fullbox("mfhd", 0, 0, u32(seq)), box("traf", tfhd, tfdt, trunBox))
	}
	m := build(0)
	m = build(uint32(len(m)) + 8)
	// one allocation for the whole fragment: the samples are copied once, into place
	out := make([]byte, 0, len(m)+8+dataLen)
	out = append(out, m...)
	out = binary.BigEndian.AppendUint32(out, uint32(8+dataLen))
	out = append(out, "mdat"...)
	for _, s := range samples {
		out = append(out, s.Data...)
	}
	return out
}
