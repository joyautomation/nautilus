// Package cameratest is a fake camera for tests: an RTSP server that serves a short
// real H.264 clip (160x120, seven seconds, a keyframe a second) as RTP interleaved
// on the control connection, and misbehaves on request the way a broken or hostile
// camera does. It imports nothing of the controller, and no production code
// imports it.
package cameratest

import (
	"bufio"
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Clip is the clip, Annex B.
//
//go:embed clip.h264
var Clip []byte

// Pictures and Keyframes are what the clip holds.
const (
	Pictures  = 70
	Keyframes = 7
)

// AU is one picture of the clip.
type AU struct {
	NALs [][]byte
	Key  bool
}

func split(d []byte) [][]byte {
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
		for len(n) > 0 && n[len(n)-1] == 0 {
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

// firstMB is first_mb_in_slice (an Exp-Golomb code at the start of a slice's
// payload): 0 starts a picture.
func firstMB(nal []byte) uint {
	zeros := 0
	pos := 8
	bit := func() uint {
		if pos>>3 >= len(nal) {
			return 1
		}
		v := uint(nal[pos>>3]>>(7-uint(pos&7))) & 1
		pos++
		return v
	}
	for bit() == 0 && zeros < 31 {
		zeros++
	}
	var v uint
	for i := 0; i < zeros; i++ {
		v = v<<1 | bit()
	}
	return (1 << uint(zeros)) - 1 + v
}

// AUs is the clip as pictures.
func AUs() []AU {
	var aus []AU
	var cur *AU
	prevSlice := false
	for _, n := range split(Clip) {
		nt := int(n[0] & 0x1f)
		isSlice := nt == 1 || nt == 5
		if cur == nil || ((nt == 7 || nt == 6 || nt == 9) && prevSlice) || (isSlice && prevSlice && firstMB(n) == 0) {
			if cur != nil {
				aus = append(aus, *cur)
			}
			cur = &AU{}
		}
		cur.NALs = append(cur.NALs, n)
		if nt == 5 {
			cur.Key = true
		}
		prevSlice = isSlice
	}
	if cur != nil {
		aus = append(aus, *cur)
	}
	return aus
}

// SPSPPS are the clip's parameter sets.
func SPSPPS() (sps, pps []byte) {
	for _, n := range split(Clip) {
		switch n[0] & 0x1f {
		case 7:
			if sps == nil {
				sps = n
			}
		case 8:
			if pps == nil {
				pps = n
			}
		}
	}
	return
}

// Packet is one RTP packet's header fields and payload.
type Packet struct {
	Marker bool
	Seq    uint16
	TS     uint32
	Body   []byte
}

// Packets packetizes pictures at mtu: single-NAL packets, an STAP-A for the
// parameter sets, FU-A for what does not fit, a marker on each picture's last.
// Pictures are 9000 ticks (a tenth of a second) apart from firstTS.
func Packets(aus []AU, mtu int, firstTS uint32, firstSeq uint16) []Packet {
	var out []Packet
	seq, ts := firstSeq, firstTS
	for _, au := range aus {
		add := func(p []byte, last bool) {
			out = append(out, Packet{Marker: last, Seq: seq, TS: ts, Body: p})
			seq++
		}
		nals := au.NALs
		for i := 0; i < len(nals); i++ {
			n := nals[i]
			last := i == len(nals)-1
			if n[0]&0x1f == 7 && i+1 < len(nals) && nals[i+1][0]&0x1f == 8 && 1+2+len(n)+2+len(nals[i+1]) <= mtu {
				m := nals[i+1]
				p := []byte{24 | n[0]&0x60, byte(len(n) >> 8), byte(len(n))}
				p = append(p, n...)
				p = append(p, byte(len(m)>>8), byte(len(m)))
				p = append(p, m...)
				i++
				add(p, i == len(nals)-1)
				continue
			}
			if len(n) <= mtu {
				add(append([]byte(nil), n...), last)
				continue
			}
			body := n[1:]
			for off := 0; off < len(body); {
				end := off + mtu - 2
				if end > len(body) {
					end = len(body)
				}
				fh := n[0] & 0x1f
				if off == 0 {
					fh |= 0x80
				}
				if end == len(body) {
					fh |= 0x40
				}
				add(append([]byte{28 | n[0]&0x60, fh}, body[off:end]...), last && end == len(body))
				off = end
			}
		}
		ts += 9000
	}
	return out
}

// SDP is a camera's session description of the clip.
func SDP(mode string) string {
	sps, pps := SPSPPS()
	fm := "a=fmtp:96 packetization-mode=1;sprop-parameter-sets=" + base64.StdEncoding.EncodeToString(sps) + "," + base64.StdEncoding.EncodeToString(pps) + "\r\n"
	switch mode {
	case "nosprop":
		fm = "a=fmtp:96 packetization-mode=1\r\n"
	case "h265":
		return "v=0\r\ns=cam\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H265/90000\r\na=control:track1\r\n"
	case "audio":
		return "v=0\r\ns=cam\r\nm=audio 0 RTP/AVP 0\r\na=control:track1\r\n"
	}
	return "v=0\r\ns=cam\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n" + fm + "a=control:track1\r\n"
}

// Opts says how the fake camera behaves.
type Opts struct {
	Auth      string // "", "digest" or "basic"
	User, Pwd string
	SDP       string        // "", "nosprop", "h265", "audio", "nosdp"
	Misbehave string        // "", "huge-body", "endless-headers", "garbage", "long-line", "play-404", "quiet", "bad-rtp", "setup-udp", "no-session"
	Loops     int           // times the clip is sent (default 1); "forever" is Loops < 0
	MTU       int           // default 1200
	Pause     time.Duration // between pictures (default none)
}

// Server is a fake camera.
type Server struct {
	ln       net.Listener
	opts     Opts
	Requests atomic.Int64
	Plays    atomic.Int64
	Basic    atomic.Int64 // requests that carried a Basic Authorization
	mu       sync.Mutex
	open     []net.Conn
}

// Start runs a fake camera until the test ends.
func Start(t testing.TB, o Opts) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{ln: ln, opts: o}
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for _, c := range s.open {
			c.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.open = append(s.open, c)
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s
}

// URL is the camera's RTSP URL for a path.
func (s *Server) URL(path string) string { return "rtsp://" + s.ln.Addr().String() + path }

// Addr is host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

func basicHeader(u, p string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
}

func md5hex(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }

func params(h string) map[string]string {
	p := map[string]string{}
	var cur strings.Builder
	q := false
	var parts []string
	for _, r := range h {
		switch {
		case r == '"':
			q = !q
			cur.WriteRune(r)
		case r == ',' && !q:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	for _, part := range parts {
		k, v, _ := strings.Cut(part, "=")
		p[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return p
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	nonce := "abc123nonce"
	for {
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		s.Requests.Add(1)
		method, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		hdr := map[string]string{}
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimRight(l, "\r\n")
			if l == "" {
				break
			}
			k, v, _ := strings.Cut(l, ":")
			hdr[strings.ToLower(k)] = strings.TrimSpace(v)
		}
		cseq := hdr["cseq"]
		reply := func(code int, extra string, body string) {
			resp := fmt.Sprintf("RTSP/1.0 %d X\r\nCSeq: %s\r\n%s", code, cseq, extra)
			if body != "" {
				resp += fmt.Sprintf("Content-Length: %d\r\nContent-Type: application/sdp\r\n", len(body))
			}
			io.WriteString(c, resp+"\r\n"+body)
		}
		if strings.HasPrefix(hdr["authorization"], "Basic ") {
			s.Basic.Add(1)
		}
		if s.opts.Auth != "" && method != "TEARDOWN" {
			a := hdr["authorization"]
			ok := false
			switch s.opts.Auth {
			case "digest":
				if strings.HasPrefix(a, "Digest ") {
					p := params(a[len("Digest "):])
					ha1 := md5hex(s.opts.User + ":cam:" + s.opts.Pwd)
					ha2 := md5hex(method + ":" + p["uri"])
					want := md5hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
					ok = p["username"] == s.opts.User && p["nonce"] == nonce && p["response"] == want
				}
			case "basic":
				ok = a == basicHeader(s.opts.User, s.opts.Pwd)
			}
			if !ok {
				ch := `Basic realm="cam"`
				if s.opts.Auth == "digest" {
					ch = `Digest realm="cam", nonce="` + nonce + `", qop="auth"`
				}
				reply(401, "WWW-Authenticate: "+ch+"\r\n", "")
				continue
			}
		}
		switch s.opts.Misbehave {
		case "bad-challenge": // a 401 whose WWW-Authenticate is not UTF-8
			reply(401, "WWW-Authenticate: \xff\xff\xff\xff\xff\xff\xff\xff digest x\r\n", "")
			continue
		case "downgrade": // digest to DESCRIBE, then a basic-only challenge to SETUP
			if method == "SETUP" && !strings.HasPrefix(hdr["authorization"], "Basic ") {
				reply(401, `WWW-Authenticate: Basic realm="cam"`+"\r\n", "")
				continue
			}
		case "huge-body":
			io.WriteString(c, "RTSP/1.0 200 OK\r\nCSeq: "+cseq+"\r\nContent-Length: 999999999\r\n\r\n")
			return
		case "endless-headers":
			io.WriteString(c, "RTSP/1.0 200 OK\r\n")
			for i := 0; i < 100000; i++ {
				if _, err := io.WriteString(c, fmt.Sprintf("X-%d: y\r\n", i)); err != nil {
					return
				}
			}
			return
		case "garbage":
			io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\nnot rtsp\r\n")
			return
		case "long-line":
			io.WriteString(c, "RTSP/1.0 200 OK\r\nX: "+strings.Repeat("a", 100000)+"\r\n\r\n")
			return
		}
		switch method {
		case "OPTIONS":
			reply(200, "Public: DESCRIBE, SETUP, PLAY, TEARDOWN\r\n", "")
		case "DESCRIBE":
			if s.opts.SDP == "nosdp" {
				reply(200, "", "")
				continue
			}
			base := "rtsp://" + s.ln.Addr().String() + "/stream/"
			if s.opts.Misbehave == "content-base" {
				base = "rtsp://evil.example:554/steal/"
			}
			reply(200, "Content-Base: "+base+"\r\n", SDP(s.opts.SDP))
		case "SETUP":
			if s.opts.Misbehave == "no-session" {
				reply(200, "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n", "")
				continue
			}
			tr := "RTP/AVP/TCP;unicast;interleaved=0-1"
			if s.opts.Misbehave == "setup-udp" {
				tr = "RTP/AVP;unicast;client_port=5000-5001"
			}
			reply(200, "Session: s12345;timeout=30\r\nTransport: "+tr+"\r\n", "")
		case "PLAY":
			s.Plays.Add(1)
			if s.opts.Misbehave == "play-404" {
				reply(404, "", "")
				continue
			}
			reply(200, "RTP-Info: url=track1;seq=1000\r\n", "")
			go s.stream(c)
		case "TEARDOWN":
			reply(200, "", "")
			return
		default:
			reply(200, "", "")
		}
	}
}

func (s *Server) stream(c net.Conn) {
	if s.opts.Misbehave == "quiet" {
		return
	}
	loops, mtu := s.opts.Loops, s.opts.MTU
	if loops == 0 {
		loops = 1
	}
	if mtu == 0 {
		mtu = 1200
	}
	aus := AUs()
	var ts uint32
	seq := uint16(1000)
	for l := 0; loops < 0 || l < loops; l++ {
		for _, p := range Packets(aus, mtu, ts, seq) {
			b := []byte{0x80, 96, byte(p.Seq >> 8), byte(p.Seq), byte(p.TS >> 24), byte(p.TS >> 16), byte(p.TS >> 8), byte(p.TS), 9, 9, 9, 9}
			if p.Marker {
				b[1] |= 0x80
			}
			b = append(b, p.Body...)
			frame := append([]byte{'$', 0, byte(len(b) >> 8), byte(len(b))}, b...)
			if s.opts.Misbehave == "bad-rtp" && p.Seq%7 == 0 {
				frame = []byte{'$', 0, 0, 3, 1, 2, 3}
			}
			if _, err := c.Write(frame); err != nil {
				return
			}
			if s.opts.Pause > 0 && p.Marker {
				time.Sleep(s.opts.Pause)
			}
		}
		seq += uint16(len(Packets(aus, mtu, 0, 0)))
		ts += uint32(len(aus)) * 9000
		time.Sleep(10 * time.Millisecond)
	}
}
