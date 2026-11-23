package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/lsp"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/runtime"
)

// A redundant pair's state stream (#710 S1b, design §4.1): the leader
// sends full images of its state (runtime.Image) to the standby, which
// applies them to a shadow of its programs (runtime.Shadow) and keeps
// their retain section in its own retain file (runtime.CarryRetained), so
// a takeover, still cold, loads the leader's retained values rather than
// its own older ones.
//
// The stream is GET <node.Prefix><cluster>/stream on the peer listener,
// behind the peer token and spareSilent like every peer call, on its own
// connection (never the votes'). The leader answers only while it leads:
// its election's gate is open and its runtime took over in that term. It
// sends a full image as soon as one is copied, then at most one every
// streamMinInterval, stretched so the images fit the budget
// (redundancy.stream-budget); it never drops one to fit. Every frame is
//
//	kind byte, interval ms (uint32), length (uint32), payload
//
// big endian, a kind of streamImage (the payload is runtime.EncodeImage's)
// or streamTooBig (the payload is the refused size, uint64).
//
// The standby takes an image only when its own voter says so
// (node.Elector.ImageTermOK: the peer's lease seen live, the term above
// what it led and not below what it holds, at most a term the peer asked
// at), applies it, and carries the newest retain section into its file
// every streamPersistEvery when it changed. A takeover stops the follower,
// flushes the newest image into the file (one fsync, timed), empties the
// shadow, and only then loads the store (runtime.SetTakeoverFlush).

const (
	streamImage  byte = 'I'
	streamTooBig byte = 'B'

	streamMinInterval  = 100 * time.Millisecond
	streamTooBigEvery  = 5 * time.Second
	streamPersistEvery = 2 * time.Second
	// streamCaptureWait bounds the leader's wait for a frame copy: a
	// task's scan period, at most.
	streamCaptureWait = 5 * time.Second
)

// pairStream is one member's state stream: the handler it serves as
// leader and the follower it runs as standby.
type pairStream struct {
	m      *pairMember
	rt     *runtime.Runtime
	shadow *runtime.Shadow
	budget int64
	build  string
	// hotMaxAge is redundancy.hot-max-age, -1 for its default (3 × lease
	// + 2 × the stream's interval).
	hotMaxAge time.Duration
	client    *http.Client
	url       string
	// ctx ends with the member (Close): every handler and the follower
	// stop with it, so a closing member's peer listener shuts down at
	// once rather than waiting on a stream that never ends by itself.
	ctx context.Context

	// The follower (run starts it while this controller stands by).
	fmu        sync.Mutex
	followStop context.CancelFunc
	followDone chan struct{}

	mu sync.Mutex
	// Standby side: the newest image applied, and what the file holds.
	state         string
	detail        string
	term, seq     uint64
	arrived       time.Time
	lagMs         int64
	intervalMs    int64
	bytes         int
	total         uint64
	layoutDiffers []string
	pending       []byte // the newest image's retain section
	pendTerm      uint64
	pendSeq       uint64
	persisted     []byte // the retain section the file last took
	persTerm      uint64
	persSeq       uint64
	persArrived   time.Time // when the image the file holds arrived
	persistErr    string    // the last carry's failure, "" once one succeeds
	staleWhy      string    // the file stands after the images, "" once a carry succeeds
	fileTerm      uint64    // the term the retain file recorded at the follower's start
	flushMs       float64
	// flushFollowing: the follower still ran when the last flush wrote
	// (tests: it must not).
	flushFollowing bool
	// tookOverAge is how old the newest image was at this controller's
	// last takeover (-1: it had none).
	tookOverAge time.Duration
	// hold, in tests, keeps the leader from sending: its standby falls
	// out of sync. held says a send loop saw it (no image is under way).
	// persistHold, in tests, keeps the standby's 2 s persist from running.
	hold, held, persistHold atomic.Bool
	// coldWhy is why this leader's takeover was cold although an image
	// existed: its condition, until a standby follows a hot-capable
	// stream again (an image of the newest format sent).
	coldWhy string
	// Leader side.
	sending   atomic.Int32
	sentTerm  uint64
	sentSeq   uint64
	sentAt    time.Time
	tooBig    int
	tooBigAt  time.Time
	leadSince time.Time
}

// streamLoadFailed is a leader's answer, and its condition, while its
// takeover could not read its retain store (review F5): it runs on initial
// values, and streaming them would overwrite the standby's file, the last
// good copy.
const streamLoadFailed = "the leader's retain store could not be read at its takeover: it runs on initial values and streams nothing, so the standby's retain file keeps the last good values; to run on them, stop this controller so the standby takes over. To keep values set here instead: stop the standby, restart this controller (it takes over from its own file, readable again), then start the standby"

// streamFormatsHeader names, on a standby's stream request, the image
// formats it reads ("1,2"). The leader sends the newest both read, and
// format 1 to a standby that names none (a build before #710 S2), so a
// pair mid rolling update still carries its retained values.
const streamFormatsHeader = "X-Nautilus-Image-Formats"

// streamFormats is this build's streamFormatsHeader value.
func streamFormats() string {
	parts := make([]string, len(runtime.ImageFormats))
	for i, f := range runtime.ImageFormats {
		parts[i] = strconv.Itoa(int(f))
	}
	return strings.Join(parts, ",")
}

// askedFormat is the format to send a standby whose request names formats.
func askedFormat(h string) byte {
	var fs []byte
	for _, p := range strings.Split(h, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 && n < 256 {
			fs = append(fs, byte(n))
		}
	}
	if mutNaut("stream_format_ignored") {
		return runtime.ImageFormats[len(runtime.ImageFormats)-1]
	}
	return runtime.ImageFormatFor(fs)
}

// streamPath is the stream's path on the peer listener.
func streamPath(cluster string) string { return node.Prefix + cluster + "/stream" }

// streamBuild names this nautilus build in an image (Image.Build): the
// release, and the commit where the binary records one.
func streamBuild() string {
	b := lsp.Version
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				b += "+" + s.Value[:12]
			}
		}
	}
	return b
}

// streamBuildFor is the build a member names in its images and checks a
// hot takeover's image against (streamBuild; tests give one member
// another).
var streamBuildFor = func(self string) string { return streamBuild() }

// startStream builds the member's state stream over rt and mounts its
// handler: call it after startLog, before the runtime and the election
// run. budget is redundancy.stream-budget in bytes a second. It runs
// until Close.
func (m *pairMember) startStream(rt *runtime.Runtime, budget int64, hotMaxAge time.Duration) *pairStream {
	s := &pairStream{
		m: m, rt: rt, shadow: rt.NewShadow(), budget: budget, build: streamBuildFor(string(m.o.Self)), hotMaxAge: hotMaxAge,
		client: node.NewPeerClient(m.o.Dial, m.o.TLS),
		url:    node.PeerURL(m.o.PeerAddr, m.o.TLS != nil) + streamPath(m.o.Cluster),
		state:  "off", tookOverAge: -1,
	}
	m.stream = s
	rt.SetTakeoverFlush(s.takeoverFlush)
	rt.SetTakeoverReport(s.tookOver)
	m.q.SetStreamStatus(s.status)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	h := http.Handler(http.HandlerFunc(s.serve))
	m.streamH.h.Store(&h)
	m.streamStop, m.streamDone = cancel, make(chan struct{})
	go func() {
		defer close(m.streamDone)
		s.run(ctx)
	}()
	return s
}

// leads reports whether this controller may send an image of term now:
// its election's gate is open in term, and its runtime took over in term.
func (s *pairStream) leads(term uint64) bool {
	if mutNaut("stream_sends_unled") {
		return true
	}
	t, lead := s.rt.LeadingTerm()
	return lead && t == term && s.m.q.IsLeader() && s.m.q.Term() == term
}

// serve is the leader's side: one standby's stream, until the leadership
// ends, the standby goes, or a write stalls.
func (s *pairStream) serve(w http.ResponseWriter, r *http.Request) {
	term, lead := s.rt.LeadingTerm()
	if !lead || !s.leads(term) {
		http.Error(w, "not leading: no state stream here", http.StatusServiceUnavailable)
		return
	}
	if s.rt.RetainLoadFailed() {
		http.Error(w, streamLoadFailed, http.StatusServiceUnavailable)
		return
	}
	format := askedFormat(r.Header.Get(streamFormatsHeader))
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-nautilus-state-stream")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	s.sending.Add(1)
	defer s.sending.Add(-1)
	bw := bufio.NewWriterSize(w, 64<<10)
	var img runtime.Image
	var buf []byte
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if !mutNaut("stream_outlives_close") {
		stop := context.AfterFunc(s.ctx, cancel) // the member closes: so does this stream
		defer stop()
	}
	next := time.Now()
	for {
		if d := time.Until(next); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		if !s.leads(term) {
			return
		}
		if s.hold.Load() {
			s.held.Store(true)
			next = time.Now().Add(streamMinInterval)
			continue
		}
		start := time.Now()
		if !s.capture(ctx, term, &img) {
			return
		}
		img.Term, img.Build = term, s.build
		ret, err := s.rt.RetainImage()
		if err != nil {
			slog.Warn("redundancy: state stream: the retain section could not be read", "err", err)
			return
		}
		img.Retain = ret
		var kind byte = streamImage
		var tb *runtime.ImageTooBigError
		buf, err = runtime.EncodeImageFormat(buf[:0], &img, format)
		switch {
		case errors.As(err, &tb):
			kind = streamTooBig
			buf = binary.BigEndian.AppendUint64(buf[:0], uint64(tb.Bytes))
		case err != nil:
			slog.Warn("redundancy: state stream: an image could not be encoded", "err", err)
			return
		}
		interval := streamMinInterval
		if kind == streamTooBig {
			interval = streamTooBigEvery // nothing to send: look again now and then
		}
		if kind == streamImage && s.budget > 0 && !mutNaut("stream_budget_drops") {
			if d := time.Duration(int64(len(buf)) * int64(time.Second) / s.budget); d > interval {
				interval = d
			}
		}
		if !s.leads(term) {
			return // the gate closed while the image was made: not this leadership's to send
		}
		_ = rc.SetWriteDeadline(time.Now().Add(max(2*time.Second, 2*interval)))
		var hdr [9]byte
		hdr[0] = kind
		binary.BigEndian.PutUint32(hdr[1:], uint32(interval/time.Millisecond))
		binary.BigEndian.PutUint32(hdr[5:], uint32(len(buf)))
		if _, err := bw.Write(hdr[:]); err != nil {
			return
		}
		if _, err := bw.Write(buf); err != nil {
			return
		}
		if err := bw.Flush(); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		s.mu.Lock()
		s.intervalMs, s.total = interval.Milliseconds(), s.total+uint64(len(buf))+9
		if kind == streamImage {
			s.sentTerm, s.sentSeq, s.sentAt, s.bytes = img.Term, img.Seq, time.Now(), len(buf)
			if format >= runtime.ImageFormats[len(runtime.ImageFormats)-1] && !mutNaut("hot_cold_condition_never_clears") {
				s.coldWhy = "" // a standby follows a stream it could take over hot from
			}
		} else {
			s.tooBig, s.tooBigAt = tb.Bytes, time.Now()
		}
		s.mu.Unlock()
		next = start.Add(interval)
	}
}

// capture asks every task for a frame copy and takes it once each has
// made one, while this controller leads in term.
func (s *pairStream) capture(ctx context.Context, term uint64, img *runtime.Image) bool {
	s.rt.RequestImage()
	deadline := time.Now().Add(streamCaptureWait)
	for !s.rt.TakeImage(img) {
		if ctx.Err() != nil || !s.leads(term) || time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

// mayFollow reports whether this controller follows a leader's stream:
// it neither leads (its election's gate nor its runtime) nor is a spare.
func (s *pairStream) mayFollow() bool {
	if _, lead := s.rt.LeadingTerm(); lead {
		return false
	}
	return !s.m.q.IsLeader() && !s.m.q.Node().Spare()
}

// run starts the follower whenever this controller stands by, and keeps
// the stream's condition on /api/cluster, until ctx ends.
func (s *pairStream) run(ctx context.Context) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	defer s.stopFollower()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.mayFollow() || mutNaut("stream_leader_follows") {
			s.startFollower(ctx)
		}
		s.m.q.SetCondition("state-stream", s.condition())
		cold, mem := s.takeoverConditions()
		s.m.q.SetCondition("takeover-cold", cold)
		s.m.q.SetCondition("retain-in-memory", mem)
	}
}

// takeoverConditions are a leader's lines in /api/cluster's warnings
// about its own takeover (#710 S2): cold although an image existed (until
// a standby follows again), and retained values held only in memory
// (until a retain save succeeds). "" when well, and on a standby.
func (s *pairStream) takeoverConditions() (cold, mem string) {
	if _, lead := s.rt.LeadingTerm(); !lead {
		return "", ""
	}
	s.mu.Lock()
	why := s.coldWhy
	s.mu.Unlock()
	if why != "" {
		cold = "the last takeover was cold although the standby held a state image (" + why + "): timers, counters, SFC steps and state tags restarted, retained values kept. This clears once a standby follows this leader's state stream again"
	}
	if s.rt.RetainOnlyInMemory() {
		mem = "this leader's retain store could not be read at its takeover, and no save has succeeded since: its retained values (the state image's) are only in memory, and a power cycle or restart loses them. Check the disk the retain file is on"
	}
	return cold, mem
}

func (s *pairStream) startFollower(ctx context.Context) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if s.followDone != nil {
		select {
		case <-s.followDone:
		default:
			return // running
		}
	}
	fctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.followStop, s.followDone = cancel, done
	go func() {
		defer close(done)
		s.follow(fctx)
	}()
}

// following reports whether the follower runs.
func (s *pairStream) following() bool {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if s.followDone == nil {
		return false
	}
	select {
	case <-s.followDone:
		return false
	default:
		return true
	}
}

// stopFollower stops the follower and waits for it (and its persist) to
// end.
func (s *pairStream) stopFollower() {
	s.fmu.Lock()
	stop, done := s.followStop, s.followDone
	s.fmu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (s *pairStream) set(state, detail string) {
	s.mu.Lock()
	s.state, s.detail = state, detail
	s.mu.Unlock()
}

// follow is the standby's side: connect, take images, persist; again
// after a break, until this controller may follow no more or ctx ends.
func (s *pairStream) follow(ctx context.Context) {
	// The file's own position is a floor too: after a restart this
	// controller remembers nothing it led or took (review F2, F4).
	if pos, err := s.rt.StreamPosition(); err == nil && pos != nil && !mutNaut("stream_file_floor_unseeded") {
		s.mu.Lock()
		s.fileTerm = max(s.fileTerm, pos.Term)
		s.mu.Unlock()
	}
	pctx, pcancel := context.WithCancel(ctx)
	pdone := make(chan struct{})
	go func() {
		defer close(pdone)
		t := time.NewTicker(streamPersistEvery)
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
				if !mutNaut("stream_persist_off") && !s.persistHold.Load() {
					s.persist(pctx)
				}
			}
		}
	}()
	defer func() { pcancel(); <-pdone }()
	retry := max(s.m.o.Lease/2, 250*time.Millisecond)
	for ctx.Err() == nil && (s.mayFollow() || mutNaut("stream_leader_follows")) {
		if err := s.followOnce(ctx); err != nil && ctx.Err() == nil {
			s.set("connecting", err.Error())
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// followOnce reads one stream connection to its end.
func (s *pairStream) followOnce(ctx context.Context) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set(node.TokenHeader, s.m.o.Token)
	req.Header.Set(streamFormatsHeader, streamFormats())
	s.mu.Lock()
	if s.state == "off" || s.state == "" {
		s.state, s.detail = "connecting", ""
	}
	s.mu.Unlock()
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("the leader's stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the peer answers %d: %s", resp.StatusCode, bytesTrim(b))
	}
	// A stream that goes quiet past twice its interval and a lease is
	// broken (a peer that froze, a link that dropped without a reset).
	idle := func(intervalMs int64) time.Duration {
		return 2*time.Duration(intervalMs)*time.Millisecond + 2*s.m.o.Lease + time.Second
	}
	watchdog := time.AfterFunc(idle(streamMinInterval.Milliseconds()), cancel)
	defer watchdog.Stop()
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var hdr [9]byte
	var body []byte
	var img runtime.Image
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return fmt.Errorf("the leader's stream ended: %w", err)
		}
		kind, intervalMs, n := hdr[0], int64(binary.BigEndian.Uint32(hdr[1:])), binary.BigEndian.Uint32(hdr[5:])
		if n > runtime.MaxImageBytes {
			return fmt.Errorf("the leader's stream sent a frame of %d bytes, over the image limit", n)
		}
		if cap(body) < int(n) {
			body = make([]byte, n)
		}
		body = body[:n]
		if _, err := io.ReadFull(br, body); err != nil {
			return fmt.Errorf("the leader's stream ended: %w", err)
		}
		watchdog.Reset(idle(intervalMs))
		if !s.mayFollow() && !mutNaut("stream_leader_follows") {
			return nil
		}
		now := time.Now()
		switch kind {
		case streamTooBig:
			size := 0
			if len(body) == 8 {
				size = int(binary.BigEndian.Uint64(body))
			}
			s.mu.Lock()
			s.state, s.detail, s.bytes, s.intervalMs = "too-big", fmt.Sprintf("the leader's image is over its size limit (%d bytes, or a string in it over 1 MiB): none is sent", size), size, intervalMs
			s.total += uint64(n) + 9
			s.mu.Unlock()
			continue
		case streamImage:
		default:
			return fmt.Errorf("the leader's stream sent a frame of kind %q", kind)
		}
		if err := runtime.DecodeImage(body, &img); err != nil {
			return fmt.Errorf("the leader's stream: %w", err)
		}
		s.take(&img, now, intervalMs, int(n))
	}
}

// take applies one image, if the term check passes.
func (s *pairStream) take(img *runtime.Image, now time.Time, intervalMs int64, n int) {
	ownLed, _ := s.rt.LeadingTerm()
	ownLed = max(ownLed, s.m.q.Term()) // the election's last epoch too
	s.mu.Lock()
	seen := max(s.term, s.persTerm, s.fileTerm)
	s.total += uint64(n) + 9
	s.intervalMs = intervalMs
	s.mu.Unlock()
	if ok, why := s.m.q.Node().ImageTermOK(img.Term, ownLed, seen); !ok && !mutNaut("stream_old_term_applied") {
		s.set("refused", fmt.Sprintf("an image of term %d: %s", img.Term, why))
		return
	}
	rep, err := s.shadow.Apply(img)
	if err != nil && len(rep.Applied) == 0 && len(rep.LayoutDiffers) == 0 {
		s.set("refused", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.term, s.seq, s.arrived, s.bytes = img.Term, img.Seq, now, n
	if len(img.Tasks) > 0 {
		s.lagMs = now.UnixMilli() - img.Tasks[0].NowMs
	}
	s.layoutDiffers = append(append(append([]string(nil), rep.LayoutDiffers...), rep.Missing...), rep.Extra...)
	s.state, s.detail = "in-sync", ""
	switch {
	case len(s.layoutDiffers) > 0:
		s.state, s.detail = "layout-differs", "this build runs another program for these tasks: a takeover would be cold for them"
	case rep.TagsDiffer:
		s.state, s.detail = "layout-differs", "the leader carries another set of tags (another manifest, or other tag types): a takeover would not carry its tags"
	case rep.TagsMissing:
		s.state, s.detail = "layout-differs", "the leader's build sends no tags (an older nautilus, mid upgrade?): a takeover would not carry its tags"
	}
	s.pending = append(s.pending[:0], img.Retain...)
	s.pendTerm, s.pendSeq = img.Term, img.Seq
	if s.persistErr != "" {
		s.state, s.detail = "not-persisted", "the retain file takes no image: "+s.persistErr
	}
	if s.staleWhy != "" {
		s.state, s.detail = "refused", s.staleWhy
	}
}

// persist carries the newest image's retain section into this
// controller's retain file, when it changed (or a new leadership's is
// not there yet). It returns the store's save time.
func (s *pairStream) persist(ctx context.Context) (time.Duration, error) {
	s.mu.Lock()
	if s.pending == nil || (s.pendTerm == s.persTerm && string(s.pending) == string(s.persisted)) {
		s.mu.Unlock()
		return 0, nil
	}
	pend := append([]byte(nil), s.pending...)
	term, seq, arrived := s.pendTerm, s.pendSeq, s.arrived
	s.mu.Unlock()
	took, err := s.rt.CarryRetained(ctx, pend, term, seq)
	switch {
	case err == nil, errors.Is(err, runtime.ErrNoRetainStore):
		s.mu.Lock()
		s.persisted, s.persTerm, s.persSeq, s.persArrived, s.persistErr, s.staleWhy = pend, term, seq, arrived, "", ""
		if s.state == "not-persisted" || s.state == "refused" {
			s.state, s.detail = "in-sync", ""
		}
		s.mu.Unlock()
		return took, nil // no retain: block: the shadow is all there is
	case errors.Is(err, runtime.ErrStaleImage) && !mutNaut("stream_stale_marked_persisted"):
		// The file stands after this image (a file kept across a reset of
		// the voter state, or restored): it holds none of it (review F2).
		pos, _ := s.rt.StreamPosition()
		why := fmt.Sprintf("the retain file stands after the leader's images (term %d, seq %d): it takes none of them", term, seq)
		if pos != nil {
			why = fmt.Sprintf("the retain file stands at term %d, seq %d, after the leader's images (term %d, seq %d): it takes none of them", pos.Term, pos.Seq, term, seq)
		}
		s.mu.Lock()
		s.staleWhy, s.state, s.detail = why, "refused", why
		s.mu.Unlock()
		return took, err
	case errors.Is(err, runtime.ErrStaleImage):
		s.mu.Lock()
		s.persisted, s.persTerm, s.persSeq = pend, term, seq
		s.mu.Unlock()
		return took, nil
	}
	if !mutNaut("stream_persist_error_hidden") {
		s.mu.Lock()
		s.persistErr = err.Error()
		s.state, s.detail = "not-persisted", "the retain file takes no image: "+err.Error()
		s.mu.Unlock()
	}
	slog.Warn("redundancy: state stream: the newest image could not be kept in the retain store", "term", term, "seq", seq, "err", err)
	return took, err
}

// takeoverFlush is the runtime's takeover flush (SetTakeoverFlush): stop
// following, carry the newest image into the retain file, take the
// shadow's copy of it as the hot candidate (#710 S2) and empty the
// shadow. The runtime loads the store after it returns, then installs the
// candidate or goes cold.
func (s *pairStream) takeoverFlush() (*runtime.HotImage, error) {
	start := time.Now()
	if !mutNaut("stream_flush_keeps_follower") {
		s.stopFollower()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.mu.Lock()
	term, seq, arrived, every := s.pendTerm, s.pendSeq, s.arrived, max(time.Duration(s.intervalMs)*time.Millisecond, streamMinInterval)
	var ret []byte
	if s.pending != nil {
		ret = append([]byte(nil), s.pending...) // the image's retain section, for an unreadable own store
	}
	s.mu.Unlock()
	following := s.following()
	var took time.Duration
	var err error
	if !mutNaut("stream_flush_no_persist") {
		took, err = s.persist(ctx)
	}
	// The one candidate: the shadow's newest image, which must be the one
	// whose retain section the file just took.
	hot := s.shadow.Take()
	if !mutNaut("stream_flush_keeps_shadow") {
		s.shadow.Reset()
	}
	if hot != nil {
		hot.Retain = ret
		hot.MaxAge = s.hotMaxAge
		if hot.MaxAge < 0 {
			hot.MaxAge = 3*s.m.o.Lease + 2*every
		}
		switch {
		case hot.Cold != "":
		case hot.Term != term || hot.Seq != seq:
			hot.Cold = fmt.Sprintf("the shadow's image (term %d, seq %d) is not the one the retain file took (term %d, seq %d)", hot.Term, hot.Seq, term, seq)
		case hot.Build != s.build && !mutNaut("hot_build_unchecked"):
			hot.Cold = fmt.Sprintf("the image is of another nautilus build (%s; this one is %s)", hot.Build, s.build)
		}
	}
	s.mu.Lock()
	// What the takeover loads: the newest image when the flush kept it,
	// else whatever the file took last.
	loaded := arrived
	if err != nil {
		loaded = s.persArrived
	}
	age := time.Duration(-1)
	if !loaded.IsZero() {
		age = start.Sub(loaded)
	}
	s.flushMs, s.tookOverAge, s.flushFollowing = float64(took.Microseconds())/1000, age, following
	// The image is spent: a later takeover (this leadership lost and won
	// again with no standby spell between) has none of its own.
	s.pending, s.persisted, s.term, s.seq = nil, nil, 0, 0
	s.pendTerm, s.pendSeq, s.arrived, s.persArrived = 0, 0, time.Time{}, time.Time{}
	s.state, s.detail, s.leadSince = "off", "", time.Now()
	s.persistErr, s.staleWhy = "", "" // a later spell as standby starts clean
	s.mu.Unlock()
	switch {
	case err != nil && age >= 0:
		slog.Warn("redundancy: takeover: the newest state image could not be kept; retained values are as of the last image the retain file took", "age", age.Round(time.Millisecond), "err", err)
	case err != nil:
		slog.Warn("redundancy: takeover: the newest state image could not be kept, and the retain file took none: retained values are this controller's own", "err", err)
	case age < 0:
		slog.Info("redundancy: takeover with no state image from a last leader: retained values are this controller's own, as it last saved or carried them")
	case age > 2*every+s.m.o.Lease:
		slog.Warn("redundancy: takeover on an old state image: retained values are as of its copy at the last leader, not newer", "age", age.Round(time.Millisecond), "term", term, "seq", seq)
	default:
		slog.Info("redundancy: takeover: the newest state image is in the retain file", "age", age.Round(time.Millisecond), "term", term, "seq", seq,
			"save", took.Round(10*time.Microsecond), "flush", time.Since(start).Round(10*time.Microsecond))
	}
	return hot, err
}

// tookOver is the runtime's report of each takeover (SetTakeoverReport):
// journalled, and a cold one with an image a condition until a standby
// follows again.
func (s *pairStream) tookOver(rep runtime.TakeoverReport) {
	s.mu.Lock()
	s.coldWhy = ""
	if !rep.Hot && rep.Image {
		s.coldWhy = rep.Reason
	}
	s.mu.Unlock()
	j := s.m.journal.Load()
	if j == nil {
		return
	}
	detail := map[string]any{"term": rep.Term, "hot": rep.Hot}
	if !rep.Hot {
		detail["reason"] = rep.Reason
	}
	if rep.ImageAge >= 0 {
		detail["imageAgeMs"] = rep.ImageAge.Milliseconds()
	}
	if rep.RetainFromImage {
		detail["retainFromImage"] = true
		detail["retain"] = "this controller's retain store could not be read: its retained values are the state image's, held only in memory until a save succeeds"
	}
	_, _ = (*j).Record(audit.Entry{
		User: "system", Source: audit.SourceSystem,
		Action: "redundancy.takeover", Target: string(s.m.o.Self),
		Detail: detail, Result: audit.ResultOK,
	})
}

// status is the stream's block of /api/cluster.
func (s *pairStream) status() *leader.StreamStatus {
	now := time.Now()
	_, rtLead := s.rt.LeadingTerm()
	lead := rtLead || s.m.q.IsLeader()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &leader.StreamStatus{AgeMs: -1, BudgetBytes: s.budget, TotalBytes: s.total, IntervalMs: s.intervalMs, FlushMs: s.flushMs, TakeoverImageAgeMs: -1}
	if s.tookOverAge >= 0 {
		st.TakeoverImageAgeMs = s.tookOverAge.Milliseconds()
	}
	if tr := s.rt.LastTakeover(); !tr.At.IsZero() {
		st.Takeover, st.TakeoverCold = "cold", tr.Reason
		if tr.Hot {
			st.Takeover, st.TakeoverCold = "hot", ""
		}
	}
	st.RetainInMemory = rtLead && s.rt.RetainOnlyInMemory()
	if lead {
		st.Role, st.State = "leader", "no-standby"
		st.Term, st.Seq, st.Bytes = s.sentTerm, s.sentSeq, s.bytes
		if s.sending.Load() > 0 {
			st.State = "sending"
		}
		if rtLead && s.rt.RetainLoadFailed() {
			st.State, st.Detail = "held", streamLoadFailed
		}
		if !s.tooBigAt.IsZero() && s.tooBigAt.After(s.sentAt) {
			st.State, st.Bytes = "too-big", s.tooBig
			st.Detail = fmt.Sprintf("the image is over its size limit (%d bytes, or a string in it over 1 MiB): none is sent, and a takeover loads the standby's own older retained values", s.tooBig)
		}
		return st
	}
	st.Role, st.State, st.Detail = "standby", s.state, s.detail
	if s.m.q.Node().Spare() {
		st.State, st.Detail = "off", "this controller is a spare"
	}
	st.Term, st.Seq, st.Bytes, st.LagMs = s.term, s.seq, s.bytes, s.lagMs
	st.PersistedTerm, st.PersistedSeq = s.persTerm, s.persSeq
	st.LayoutDiffers = s.layoutDiffers
	if !s.arrived.IsZero() {
		st.AgeMs = now.Sub(s.arrived).Milliseconds()
		every := max(time.Duration(s.intervalMs)*time.Millisecond, streamMinInterval)
		if (st.State == "in-sync" || st.State == "layout-differs") && now.Sub(s.arrived) > 2*every+s.m.o.Lease {
			st.State, st.Detail = "behind", fmt.Sprintf("no image for %s", now.Sub(s.arrived).Round(time.Millisecond))
		}
	}
	return st
}

// condition is the stream's line in /api/cluster's warnings ("" while it
// is well): a standby not in sync, a leader no standby follows (after two
// leases, so a start is not a fault), an image too big to send.
func (s *pairStream) condition() string {
	st := s.status()
	age := "no image yet"
	if st.AgeMs >= 0 {
		age = fmt.Sprintf("the newest image is %s old", (time.Duration(st.AgeMs) * time.Millisecond).Round(time.Millisecond))
	}
	switch st.State {
	case "in-sync", "sending", "off":
		return ""
	case "no-standby":
		s.mu.Lock()
		since := s.leadSince
		s.mu.Unlock()
		if since.IsZero() || time.Since(since) < 2*s.m.o.Lease {
			return ""
		}
		return "state stream: no standby is following this leader: a takeover now would load the standby's own older retained values"
	case "too-big", "held":
		return "state stream: " + st.Detail
	case "layout-differs":
		return fmt.Sprintf("state stream: tasks %v run another program here than on the leader: their frames are not kept (%s)", st.LayoutDiffers, age)
	}
	msg := "state stream " + st.State
	if st.Detail != "" {
		msg += ": " + st.Detail
	}
	return msg + "; a takeover now would load retained values as of the newest image kept (" + age + ")"
}

func bytesTrim(b []byte) string {
	const max = 200
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}
