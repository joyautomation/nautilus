//go:build !js

package camera

import (
	"bytes"
	"testing"

	"github.com/joyautomation/nautilus/camera/cameratest"
)

// The test clip: seven seconds of a 160x120 test pattern, ten pictures a second,
// baseline H.264, a keyframe a second, made with ffmpeg (cameratest/clip.h264,
// Annex B). The relay's tests run on real encoded NAL units, so the parameter
// sets, slices and keyframes are what a camera sends.

func splitAnnexB(d []byte) [][]byte {
	var nals [][]byte
	i := bytes.Index(d, []byte{0, 0, 1})
	for i >= 0 {
		start := i + 3
		j := bytes.Index(d[start:], []byte{0, 0, 1})
		end := len(d)
		if j >= 0 {
			end = start + j
		}
		n := d[start:end]
		for len(n) > 0 && n[len(n)-1] == 0 { // the zero of a 4-byte start code
			n = n[:len(n)-1]
		}
		if len(n) > 0 {
			nals = append(nals, n)
		}
		if j < 0 {
			break
		}
		i = start + j
	}
	return nals
}

// firstMB is first_mb_in_slice of a slice NAL: 0 starts a picture.
func firstMB(nal []byte) uint {
	r := &bitReader{b: unescape(nal[1:])}
	return r.ue()
}

// clipAUs is the clip as access units.
func clipAUs(t testing.TB) []AccessUnit {
	t.Helper()
	d := cameratest.Clip
	var aus []AccessUnit
	var cur *AccessUnit
	flush := func() {
		if cur != nil {
			aus = append(aus, *cur)
		}
		cur = nil
	}
	prevSlice := false
	for _, n := range splitAnnexB(d) {
		nt := nalType(n[0])
		isSlice := nt == nalSlice || nt == nalIDR
		newAU := cur == nil || ((nt == nalSPS || nt == nalSEI || nt == nalAUD) && prevSlice) || (isSlice && prevSlice && firstMB(n) == 0)
		if newAU {
			flush()
			cur = &AccessUnit{TS: uint32(len(aus)) * 9000} // 10 pictures a second at 90 kHz
		}
		cur.NALs = append(cur.NALs, n)
		cur.Size += len(n)
		if nt == nalIDR {
			cur.Key = true
		}
		prevSlice = isSlice
	}
	flush()
	return aus
}

// clipPackets packetizes access units the way a camera does at mtu: single-NAL
// packets, an STAP-A for the parameter sets, FU-A for what does not fit.
func clipPackets(aus []AccessUnit, mtu int) []RTPPacket {
	var out []RTPPacket
	seq := uint16(1000)
	add := func(ts uint32, payload []byte, last bool) {
		out = append(out, RTPPacket{Marker: last, PT: 96, Seq: seq, TS: ts, Payload: payload})
		seq++
	}
	for _, au := range aus {
		type unit struct {
			nal []byte
		}
		nals := au.NALs
		for i := 0; i < len(nals); i++ {
			last := i == len(nals)-1
			n := nals[i]
			if nalType(n[0]) == nalSPS && i+1 < len(nals) && nalType(nals[i+1][0]) == nalPPS && 1+2+len(n)+2+len(nals[i+1]) <= mtu {
				p := []byte{nalSTAPA | n[0]&0x60}
				p = append(p, byte(len(n)>>8), byte(len(n)))
				p = append(p, n...)
				m := nals[i+1]
				p = append(p, byte(len(m)>>8), byte(len(m)))
				p = append(p, m...)
				i++
				add(au.TS, p, i == len(nals)-1)
				continue
			}
			if len(n) <= mtu {
				add(au.TS, append([]byte(nil), n...), last)
				continue
			}
			body := n[1:]
			for off := 0; off < len(body); {
				end := off + mtu - 2
				if end > len(body) {
					end = len(body)
				}
				h := n[0] & 0x1f
				fh := h
				if off == 0 {
					fh |= 0x80
				}
				if end == len(body) {
					fh |= 0x40
				}
				p := []byte{nalFUA | n[0]&0x60, fh}
				p = append(p, body[off:end]...)
				add(au.TS, p, last && end == len(body))
				off = end
			}
		}
	}
	return out
}

func TestTheClipIsRealH264(t *testing.T) {
	aus := clipAUs(t)
	if len(aus) != 70 {
		t.Fatalf("%d pictures, want 70", len(aus))
	}
	keys := 0
	for _, au := range aus {
		if au.Key {
			keys++
		}
	}
	if keys != 7 {
		t.Errorf("%d keyframes, want 7", keys)
	}
	var sps []byte
	for _, n := range aus[0].NALs {
		if nalType(n[0]) == nalSPS {
			sps = n
		}
	}
	info, err := ParseSPS(sps)
	if err != nil || info.Width != 160 || info.Height != 120 || info.Profile != 66 {
		t.Errorf("the SPS: %+v %v, want 160x120 baseline", info, err)
	}
}
