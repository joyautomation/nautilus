//go:build !js

package camera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HLS (RFC 8216) of one camera's H.264, in fragmented MP4: a sliding playlist of
// whole-GOP segments cut at keyframes, an init segment, no transcoding.
//
// Memory is bounded per stream whatever a camera sends: the finished segments
// are at most hlsWindow of them and hlsMaxWindowBytes in all; the segment being
// built is at most hlsMaxSegBytes (beyond that the picture run is dropped and the
// stream waits for the next keyframe); the depacketiser holds at most one access
// unit (maxAUBytes) and one fragmented NAL (maxNALBytes).

const (
	hlsTarget         = 2 * time.Second // segments are cut at the first keyframe after this
	hlsWindow         = 6               // finished segments listed
	hlsMaxWindowBytes = 24 << 20
	hlsMaxSegBytes    = 8 << 20
	hlsMaxSegDur      = 20 * time.Second // a run with no keyframe this long is dropped
	hlsReadyAfter     = 2                // segments before a playlist is served
)

// StreamMemoryBound is the most memory one stream's buffers hold: the window, the
// segment being built, one access unit and one reassembled NAL unit.
const StreamMemoryBound = hlsMaxWindowBytes + hlsMaxSegBytes + maxAUBytes + maxNALBytes

type hlsSeg struct {
	seq  uint64
	data []byte
	dur  float64 // seconds
}

// HLSStream is one camera's segmenter and its served state. Feed is called by one
// goroutine; the readers (Playlist, Segment, Init) by any.
type HLSStream struct {
	mu       sync.Mutex
	init     []byte
	sps, pps []byte
	segs     []hlsSeg
	bytes    int
	nextSeq  uint64
	ready    chan struct{}
	closed   bool
	failed   error

	// the segment being built
	cur        []Sample
	curBytes   int
	curStart   time.Duration // by RTP timestamp, since the stream began
	curKey     bool
	pendingAU  *AccessUnit
	baseTS     uint32
	haveBase   bool
	lastDur    uint32
	decodeTime uint64
	fragSeq    uint32
	dropped    int
}

// NewHLSStream returns a stream with no parameter sets yet: SetParams makes its
// init segment, and pictures fed before that are dropped.
func NewHLSStream() *HLSStream {
	return &HLSStream{ready: make(chan struct{}), nextSeq: 1, lastDur: VideoTimescale / 10}
}

// SetParams makes the init segment from the stream's sequence and picture
// parameter sets. Called again with the same sets it does nothing; with different
// ones (the camera changed its size or profile) it ends the stream: a player's
// buffers cannot follow that.
func (s *HLSStream) SetParams(sps, pps []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.init != nil {
		if bytes.Equal(sps, s.sps) && bytes.Equal(pps, s.pps) {
			return nil
		}
		s.closeLocked(errors.New("the camera changed its picture settings: reopen the stream"))
		return s.failed
	}
	info, err := ParseSPS(sps)
	if err != nil {
		return err
	}
	init, err := InitSegment(sps, pps, info)
	if err != nil {
		return err
	}
	s.init, s.sps, s.pps = init, append([]byte(nil), sps...), append([]byte(nil), pps...)
	return nil
}

// Feed takes the next access unit. Pictures before the first keyframe, and runs
// that cannot be segmented (too big, too long without a keyframe), are dropped.
func (s *HLSStream) Feed(au AccessUnit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.init == nil {
		if !s.closed {
			s.dropped++
		}
		return
	}
	// a picture's duration is known when the next one arrives
	if prev := s.pendingAU; prev != nil {
		d := au.TS - prev.TS // wraps correctly at 2^32
		if d == 0 || d > 5*VideoTimescale {
			d = s.lastDur
		}
		s.lastDur = d
		s.addSample(*prev, d)
	}
	cp := au
	s.pendingAU = &cp
	// a keyframe is where a finished segment is cut
	if au.Key && len(s.cur) > 0 && s.curDuration() >= hlsTarget {
		s.cut()
	}
}

func (s *HLSStream) curDuration() time.Duration {
	var t uint64
	for _, sm := range s.cur {
		t += uint64(sm.Duration)
	}
	return time.Duration(t) * time.Second / VideoTimescale
}

// addSample puts a picture in the segment being built, starting one at a keyframe.
func (s *HLSStream) addSample(au AccessUnit, dur uint32) {
	if len(s.cur) == 0 && !au.Key {
		s.dropped++
		return
	}
	data := AVCC(au.NALs)
	if len(data) == 0 {
		return
	}
	if s.curBytes+len(data) > hlsMaxSegBytes || s.curDuration() > hlsMaxSegDur {
		// a stream that cannot be cut: drop the run and wait for the next keyframe
		s.cur, s.curBytes = nil, 0
		s.dropped++
		if !au.Key {
			return
		}
	}
	s.cur = append(s.cur, Sample{Data: data, Duration: dur, Key: au.Key})
	s.curBytes += len(data)
}

// cut finishes the segment being built.
func (s *HLSStream) cut() {
	if len(s.cur) == 0 {
		return
	}
	// the sample list belongs to the segment now; the pending picture (the keyframe
	// that triggered the cut) starts the next one when it is added
	var dur uint64
	for _, sm := range s.cur {
		dur += uint64(sm.Duration)
	}
	s.fragSeq++
	data := Fragment(s.fragSeq, s.decodeTime, s.cur)
	s.decodeTime += dur
	seg := hlsSeg{seq: s.nextSeq, data: data, dur: float64(dur) / VideoTimescale}
	s.nextSeq++
	s.segs = append(s.segs, seg)
	s.bytes += len(data)
	s.cur, s.curBytes = nil, 0
	for len(s.segs) > hlsWindow || (s.bytes > hlsMaxWindowBytes && len(s.segs) > 2) {
		s.bytes -= len(s.segs[0].data)
		s.segs = s.segs[1:]
	}
	if len(s.segs) >= hlsReadyAfter && s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
}

// Close ends the stream: readers are told it is over (err says why, nil for a
// stop). Playlist and Segment answer nothing after it.
func (s *HLSStream) Close(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(err)
}

func (s *HLSStream) closeLocked(err error) {
	if s.closed {
		return
	}
	s.closed, s.failed = true, err
	if s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
}

// Err says why a closed stream ended.
func (s *HLSStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Wait blocks until the first segments are ready, the stream ends or ctx does.
func (s *HLSStream) Wait(ctx context.Context) error {
	s.mu.Lock()
	r := s.ready
	s.mu.Unlock()
	if r != nil {
		select {
		case <-r:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if s.failed != nil {
			return s.failed
		}
		return errors.New("the stream has ended")
	}
	return nil
}

// Init is the init segment, nil before the parameter sets are known.
func (s *HLSStream) Init() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.init
}

// Playlist is the media playlist as it stands, "" before there are segments.
func (s *HLSStream) Playlist() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.segs) < hlsReadyAfter || s.closed {
		return ""
	}
	maxd := 0.0
	for _, g := range s.segs {
		maxd = math.Max(maxd, g.dur)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(maxd)))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", s.segs[0].seq)
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	for _, g := range s.segs {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg-%d.m4s\n", g.dur, g.seq)
	}
	return b.String()
}

// Segment is the finished segment seq, if it is still in the window.
func (s *HLSStream) Segment(seq uint64) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.segs {
		if g.seq == seq {
			return g.data, true
		}
	}
	return nil, false
}

// Stats are what is held now, for tests and the journal.
func (s *HLSStream) Stats() (segments, bytes, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.segs), s.bytes + s.curBytes, s.dropped
}

// ParseSegName reads "seg-<n>.m4s"; anything else is not a segment name.
func ParseSegName(name string) (uint64, bool) {
	n, ok := strings.CutPrefix(name, "seg-")
	if !ok {
		return 0, false
	}
	n, ok = strings.CutSuffix(n, ".m4s")
	if !ok || n == "" || len(n) > 18 || (len(n) > 1 && n[0] == '0') {
		return 0, false
	}
	v, err := strconv.ParseUint(n, 10, 64)
	return v, err == nil
}
