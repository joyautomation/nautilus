//go:build !js

package camera

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/joyautomation/nautilus/camera/cameratest"
)

// Fuzz targets for everything a camera's bytes reach first: the session
// description, the parameter sets, RTP packets and their reassembly, RTSP
// responses. Each is seeded from the clip and the fake camera; `go test` runs the
// seeds (and any crasher kept in testdata/fuzz as a regression test), and
// `go test -fuzz=FuzzX -fuzztime=...` explores.

func seedSDP(t testing.TB) string { return cameratest.SDP("") }

func FuzzParseSDP(f *testing.F) {
	f.Add(seedSDP(f))
	f.Add("v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 sprop-parameter-sets=Z0IAHg==,aM4=\r\n")
	f.Add("m=video 0 RTP/AVP 999\na=rtpmap:999 H264/0\n")
	f.Add("a=control\nm=\nm=video\nm=audio 0 RTP/AVP 0\n")
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParseSDP(s)
		if err != nil {
			return
		}
		if m.ClockRate <= 0 || m.PayloadType < 0 || m.PayloadType > 127 || len(m.SPS) > 4096 || len(m.PPS) > 4096 {
			t.Fatalf("an accepted track out of bounds: %+v", m)
		}
	})
}

func FuzzParseSPS(f *testing.F) {
	for _, au := range clipAUs(f)[:1] {
		for _, n := range au.NALs {
			if nalType(n[0]) == nalSPS {
				f.Add(n)
			}
		}
	}
	b, _ := base64.StdEncoding.DecodeString("Z2QAKKwsaAeAIv8AEAAQAAADABAAAAMDwQAHXAAB1cLO5oHhEJrA")
	f.Add(b)
	f.Add([]byte{0x67, 100, 0, 40, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := ParseSPS(b)
		if err != nil {
			return
		}
		if info.Width < 1 || info.Width > 8192 || info.Height < 1 || info.Height > 8192 {
			t.Fatalf("an accepted size out of bounds: %+v", info)
		}
		// an accepted set must make an init segment
		if _, err := InitSegment(b, []byte{nalPPS | 0x60, 1}, info); err != nil {
			t.Fatalf("an accepted SPS did not make an init segment: %v", err)
		}
	})
}

func FuzzParseRTP(f *testing.F) {
	for _, p := range clipPackets(clipAUs(f)[:3], 400) {
		b := append([]byte{0x80, p.PT, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}, p.Payload...)
		f.Add(b)
	}
	f.Add([]byte{0xb0, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0xbe, 0xde, 0, 255})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := ParseRTP(b)
		if err != nil {
			return
		}
		if len(p.Payload) > len(b) {
			t.Fatal("a payload longer than the packet")
		}
	})
}

// FuzzDepacketizer feeds the depacketiser a stream of packets cut from the bytes:
// FU-A and STAP-A reassembly under any loss, order and length. The bounds hold and
// nothing panics.
func FuzzDepacketizer(f *testing.F) {
	var seed []byte
	for _, p := range clipPackets(clipAUs(f)[:6], 100) {
		seed = append(seed, byte(len(p.Payload)>>8), byte(len(p.Payload)), byte(p.Seq), byte(p.TS>>8))
		if p.Marker {
			seed[len(seed)-3] |= 0 // the marker comes from the data below
		}
		seed = append(seed, p.Payload...)
	}
	f.Add(seed)
	f.Add(bytes.Repeat([]byte{28, 0x80, 5, 1, 2, 3}, 40))
	f.Add([]byte{0, 3, 1, 1, 24, 0, 0, 0, 4, 1, 1, 24, 0, 1, 255})
	f.Fuzz(func(t *testing.T, b []byte) {
		d := &Depacketizer{}
		emitted := 0
		emit := func(au AccessUnit) {
			emitted++
			if au.Size > maxAUBytes || len(au.NALs) > maxNALsPerAU {
				t.Fatalf("an access unit out of bounds: %d bytes, %d NALs", au.Size, len(au.NALs))
			}
			for _, n := range au.NALs {
				if len(n) == 0 || len(n) > maxNALBytes {
					t.Fatalf("a NAL of %d bytes", len(n))
				}
			}
		}
		seq, ts := uint16(0), uint32(0)
		for len(b) >= 4 {
			n := int(b[0])<<8 | int(b[1])
			if n > len(b)-4 {
				n = len(b) - 4
			}
			seq += uint16(b[2] % 5) // 0 repeats, 2..4 lose packets, 1 is in order
			if b[3]&1 == 0 {
				ts += uint32(b[3]) + 1
			}
			p := RTPPacket{Marker: b[3]&2 != 0, Seq: seq, TS: ts, Payload: b[4 : 4+n]}
			d.Push(p, emit)
			if len(d.fu) > maxNALBytes {
				t.Fatalf("the fragment buffer holds %d bytes", len(d.fu))
			}
			b = b[4+n:]
		}
	})
}

func FuzzReadRTSP(f *testing.F) {
	f.Add([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 5\r\n\r\nhello"))
	f.Add([]byte("RTSP/1.0 401 Unauthorized\r\nWWW-Authenticate: Digest realm=\"a\", nonce=\"b\"\r\nWWW-Authenticate: Basic realm=\"a\"\r\n\r\n"))
	f.Add([]byte("$\x00\x00\x04abcdRTSP/1.0 200 OK\r\n\r\n"))
	f.Add([]byte("RTSP/1.0 200 OK\r\nContent-Length: -1\r\n\r\n"))
	f.Add([]byte("RTSP/x 99999 \r\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bufio.NewReader(bytes.NewReader(b))
		for i := 0; i < 64; i++ {
			fr, resp, err := readRTSP(r)
			if err != nil {
				return
			}
			if fr != nil && len(fr.Payload) > 65535 {
				t.Fatal("a frame longer than 16 bits")
			}
			if resp != nil && (len(resp.Body) > rtspMaxBody || len(resp.Header) > rtspMaxHeaders+1 || resp.Code < 100 || resp.Code > 599) {
				t.Fatalf("a response out of bounds: %d, %d headers, %d body", resp.Code, len(resp.Header), len(resp.Body))
			}
		}
	})
}

func FuzzParseSegName(f *testing.F) {
	for _, s := range []string{"seg-1.m4s", "seg-0.m4s", "seg-01.m4s", "../seg-1.m4s", "seg-99999999999999999999.m4s"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, ok := ParseSegName(s); ok {
			// whatever parses is exactly what the playlist writes
			if want := "seg-" + itoa(n) + ".m4s"; want != s {
				t.Fatalf("%q parsed as %d but the playlist would say %q", s, n, want)
			}
		}
	})
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
