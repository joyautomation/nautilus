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
// Memory is bounded per stream whatever a camera sends, and counted against a
// budget shared by every live stream of the controller (MemBudget): what a stream
// holds is the finished segments (hlsWindow of them, hlsMaxWindowBytes in all), the
// segment being built (hlsMaxSegBytes and hlsMaxSegSamples, counting each picture's
// bookkeeping as well as its bytes), the picture waiting for its duration, and a
// fixed reservation for the depacketiser's buffers (one picture, one fragmented NAL
// unit) and the frames read before PLAY is answered. A stream that would pass its
// own bound or the controller's ends with a reason.

const (
	hlsTarget         = 2 * time.Second // segments are cut at the first keyframe after this
	hlsWindow         = 6               // finished segments listed
	hlsMaxWindowBytes = 16 << 20
	// hlsMaxSegBytes is the most one segment holds: a whole group of pictures. At a
	// two-second keyframe interval that is 16 Mbit/s, at four seconds 8 Mbit/s, which
	// is more than 1080p needs in H.264.
	hlsMaxSegBytes   = 4 << 20
	hlsMaxSegSamples = 1500
	hlsMaxSegDur     = 20 * time.Second // a run with no keyframe this long is dropped
	hlsReadyAfter    = 2                // segments before a playlist is served
	sampleOverhead   = 64               // bookkeeping counted for each picture (the Sample, its trun entry)
	nalOverhead      = 32
	// preplayFrames is how many interleaved frames read before PLAY is answered are kept.
	preplayFrames = 32
)

// StreamFixedBytes is what each stream reserves up front for the buffers the
// depacketiser and the RTSP reader use: one picture, one fragmented NAL unit and the
// frames read before PLAY.
const StreamFixedBytes = maxAUBytes + maxNALBytes + preplayFrames*65536

// StreamMemoryBound is the most memory one stream holds at the very most: the
// fixed buffers, the window, the segment being built and the picture waiting for
// its duration.
const StreamMemoryBound = StreamFixedBytes + hlsMaxWindowBytes + hlsMaxSegBytes + maxAUBytes

// DefaultLiveBudget is the memory all live streams of a controller may hold
// together unless the manifest says otherwise.
const DefaultLiveBudget = 64 << 20

// ErrStreamTooLarge ends a stream that would hold more memory than the controller
// allows its live streams.
var ErrStreamTooLarge = errors.New("camera stream too large for this controller: it would use more memory than the live-video budget allows")

// ErrKeyframes ends a stream whose groups of pictures do not fit a segment.
var ErrKeyframes = errors.New("the camera's keyframes are too far apart for its bitrate (a segment would pass 4 MB): set its keyframe interval to 2 seconds, or a lower bitrate")

// MemBudget is the memory the live streams of a controller share.
type MemBudget struct {
	mu          sync.Mutex
	limit, used int64
}

// NewMemBudget makes a budget of limit bytes (DefaultLiveBudget when 0).
func NewMemBudget(limit int64) *MemBudget {
	if limit <= 0 {
		limit = DefaultLiveBudget
	}
	return &MemBudget{limit: limit}
}

func (b *MemBudget) reserve(n int64) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

func (b *MemBudget) release(n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

// Used is what the streams hold now.
func (b *MemBudget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

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

	budget *MemBudget
	held   int64 // what this stream has reserved in the budget

	// the segment being built
	cur        []Sample
	curBytes   int // the pictures' bytes and their bookkeeping
	curTicks   uint64
	pendingAU  *AccessUnit
	pendingCnt int64 // what the pending picture was counted as
	lastDur    uint32
	decodeTime uint64
	fragSeq    uint32
	dropped    int
	overflows  int
}

// NewHLSStream returns a stream whose memory is counted in budget (nil: not
// counted) and which has no parameter sets yet: SetParams makes its init segment,
// and pictures fed before that are dropped.
func NewHLSStream(budget *MemBudget) *HLSStream {
	s := &HLSStream{ready: make(chan struct{}), nextSeq: 1, lastDur: VideoTimescale / 10, budget: budget}
	if !s.reserveLocked(StreamFixedBytes) {
		s.closeLocked(ErrStreamTooLarge)
	}
	return s
}

// reserveLocked takes n bytes of the budget for this stream, false when there are none.
func (s *HLSStream) reserveLocked(n int64) bool {
	if !s.budget.reserve(n) {
		return false
	}
	s.held += n
	return true
}

func (s *HLSStream) giveBackLocked(n int64) {
	if n > s.held {
		n = s.held
	}
	s.held -= n
	s.budget.release(n)
}

// SetParams makes the init segment from the stream's sequence and picture
// parameter sets. Called again with the same sets it does nothing; with different
// ones before any picture has been taken (the description's sets against the
// stream's own) it replaces them; with different ones later (the camera changed its
// size or profile) it ends the stream: a player's buffers cannot follow that.
func (s *HLSStream) SetParams(sps, pps []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.failed
	}
	if s.init != nil {
		if bytes.Equal(sps, s.sps) && bytes.Equal(pps, s.pps) {
			return nil
		}
		if s.nextSeq != 1 || len(s.cur) > 0 || s.pendingAU != nil {
			s.closeLocked(errors.New("the camera changed its picture settings: reopen the stream"))
			return s.failed
		}
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

func auCost(au AccessUnit) int64 { return int64(au.Size) + int64(len(au.NALs))*nalOverhead }

// Feed takes the next access unit. Pictures before the first keyframe, and runs
// that cannot be segmented, are dropped.
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
		s.pendingAU = nil
		s.giveBackLocked(s.pendingCnt)
		s.pendingCnt = 0
		s.addSample(*prev, d)
		if s.closed {
			return
		}
	}
	cost := auCost(au)
	if !s.reserveLocked(cost) {
		s.closeLocked(ErrStreamTooLarge)
		return
	}
	cp := au
	s.pendingAU, s.pendingCnt = &cp, cost
	// a keyframe is where a finished segment is cut
	if au.Key && len(s.cur) > 0 && time.Duration(s.curTicks)*time.Second/VideoTimescale >= hlsTarget {
		s.cut()
	}
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
	cost := int64(len(data) + sampleOverhead)
	over := s.curBytes+int(cost) > hlsMaxSegBytes || len(s.cur) >= hlsMaxSegSamples ||
		time.Duration(s.curTicks+uint64(dur))*time.Second/VideoTimescale > hlsMaxSegDur
	if over {
		// a group of pictures that does not fit a segment: drop the run and wait for the
		// next keyframe; a camera that does it twice never will fit
		s.dropRunLocked()
		s.dropped++
		s.overflows++
		if s.overflows >= 2 {
			s.closeLocked(ErrKeyframes)
			return
		}
		if !au.Key {
			return
		}
	}
	if !s.reserveLocked(cost) {
		s.closeLocked(ErrStreamTooLarge)
		return
	}
	s.cur = append(s.cur, Sample{Data: data, Duration: dur, Key: au.Key})
	s.curBytes += int(cost)
	s.curTicks += uint64(dur)
}

func (s *HLSStream) dropRunLocked() {
	s.giveBackLocked(int64(s.curBytes))
	s.cur, s.curBytes, s.curTicks = nil, 0, 0
}

// cut finishes the segment being built.
func (s *HLSStream) cut() {
	if len(s.cur) == 0 {
		return
	}
	s.fragSeq++
	data := Fragment(s.fragSeq, s.decodeTime, s.cur)
	// the segment's bytes replace the pictures' (the fragment is built once, so for a
	// moment both exist: that is the transient the fixed reservation does not cover,
	// and is at most hlsMaxSegBytes)
	if !s.reserveLocked(int64(len(data))) {
		s.closeLocked(ErrStreamTooLarge)
		return
	}
	s.decodeTime += s.curTicks
	seg := hlsSeg{seq: s.nextSeq, data: data, dur: float64(s.curTicks) / VideoTimescale}
	s.nextSeq++
	s.segs = append(s.segs, seg)
	s.bytes += len(data)
	s.dropRunLocked()
	for len(s.segs) > hlsWindow || (s.bytes > hlsMaxWindowBytes && len(s.segs) > 2) {
		old := s.segs[0]
		s.bytes -= len(old.data)
		s.giveBackLocked(int64(len(old.data)))
		// the slot is cleared and the list copied, so the dropped segment is not kept
		// alive by the backing array
		s.segs = append([]hlsSeg(nil), s.segs[1:]...)
	}
	if len(s.segs) >= hlsReadyAfter && s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
}

// Close ends the stream: readers are told it is over (err says why, nil for a
// stop). Playlist and Segment answer nothing after it, and what it held is given
// back to the budget.
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
	s.cur, s.curBytes, s.curTicks, s.pendingAU, s.segs, s.bytes = nil, 0, 0, nil, nil, 0
	s.giveBackLocked(s.held)
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

// Held is what this stream has reserved in the budget.
func (s *HLSStream) Held() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
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
