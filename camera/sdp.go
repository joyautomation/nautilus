//go:build !js

package camera

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// Media is the H.264 video track of an RTSP session description (RFC 4566,
// RFC 6184): the first video section that carries H.264.
type Media struct {
	Control     string // the track's control URL, absolute or relative to the base
	PayloadType int
	ClockRate   int
	SPS, PPS    []byte // from sprop-parameter-sets, NAL units with their header byte
	PacketMode  int
}

const (
	maxSDPBytes = 32 << 10
	maxSDPLines = 512
)

// ParseSDP finds the H.264 video track. It never fails on a section it does not
// want, and says why when there is no track it can relay (another codec, no video).
func ParseSDP(sdp string) (Media, error) {
	if len(sdp) > maxSDPBytes {
		return Media{}, errors.New("sdp: the description is far too long")
	}
	lines := strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n")
	if len(lines) > maxSDPLines {
		return Media{}, errors.New("sdp: the description has far too many lines")
	}
	var cur *Media
	var curIsVideo bool
	var other []string
	var found *Media
	otherVideo := false
	flush := func() {
		if cur == nil || !curIsVideo {
			return
		}
		if cur.PayloadType >= 0 && found == nil && cur.ClockRate > 0 && h264Seen(cur) {
			c := *cur
			found = &c
		} else if found == nil {
			otherVideo = true
		}
	}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if len(ln) < 2 || ln[1] != '=' {
			continue
		}
		val := ln[2:]
		switch ln[0] {
		case 'm':
			flush()
			cur = &Media{PayloadType: -1}
			f := strings.Fields(val)
			curIsVideo = len(f) >= 4 && f[0] == "video" && f[2] == "RTP/AVP"
			if curIsVideo {
				if pt, err := strconv.Atoi(f[3]); err == nil && pt >= 0 && pt < 128 {
					cur.PayloadType = pt
				}
			} else if len(f) > 0 {
				other = append(other, f[0])
			}
		case 'a':
			if cur == nil || !curIsVideo {
				continue
			}
			name, arg, _ := strings.Cut(val, ":")
			switch name {
			case "control":
				cur.Control = strings.TrimSpace(arg)
			case "rtpmap":
				// <pt> H264/90000
				f := strings.Fields(arg)
				if len(f) == 2 {
					pt, err := strconv.Atoi(f[0])
					enc, rate, _ := strings.Cut(f[1], "/")
					if err == nil && pt == cur.PayloadType && strings.EqualFold(enc, "H264") {
						if r, err := strconv.Atoi(rate); err == nil && r > 0 && r <= 1000000 {
							cur.ClockRate = r
						}
					}
				}
			case "fmtp":
				// <pt> packetization-mode=1;sprop-parameter-sets=<b64>,<b64>;...
				pt, params, _ := strings.Cut(strings.TrimSpace(arg), " ")
				if n, err := strconv.Atoi(pt); err != nil || n != cur.PayloadType {
					continue
				}
				for _, kv := range strings.Split(params, ";") {
					k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
					switch strings.ToLower(k) {
					case "packetization-mode":
						cur.PacketMode, _ = strconv.Atoi(v)
					case "sprop-parameter-sets":
						for _, p := range strings.Split(v, ",") {
							b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
							if err != nil || len(b) == 0 || len(b) > 4096 {
								continue
							}
							switch nalType(b[0]) {
							case nalSPS:
								cur.SPS = b
							case nalPPS:
								cur.PPS = b
							}
						}
					}
				}
			}
		}
	}
	flush()
	if found == nil {
		if otherVideo {
			return Media{}, errors.New("sdp: no H.264 video track (the camera offers another video codec: set its stream to H.264)")
		}
		if len(other) > 0 {
			return Media{}, errors.New("sdp: no H.264 video track: the camera offers no video")
		}
		return Media{}, errors.New("sdp: the camera offers no video track")
	}
	return *found, nil
}

func h264Seen(m *Media) bool { return m.ClockRate > 0 }
