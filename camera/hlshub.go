//go:build !js

package camera

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// HLSIdle is how long a camera's RTSP session lives after the last request for its
// playlist or a segment: a stream with no viewer costs the camera and the
// controller nothing.
var HLSIdle = 30 * time.Second

// HLSSession is one camera's running RTSP-to-HLS pipeline.
type HLSSession struct {
	Stream *HLSStream
	cancel context.CancelFunc
	done   chan struct{}
	last   time.Time
}

// HLSHub starts a camera's session on the first request and stops it when it is
// idle. Sessions are at most one per camera.
type HLSHub struct {
	f        *Fetcher
	budget   *MemBudget
	mu       sync.Mutex
	sessions map[string]*HLSSession
	failed   map[string]hlsFailure
	reaping  bool
}

type hlsFailure struct {
	err   error
	until time.Time
}

// HLSRetry is how long a camera that failed is not opened again: a viewer's
// player asking again every second must not redial a broken camera that often.
var HLSRetry = 5 * time.Second

// ErrTooManyStreams says the controller already has as many camera sessions open as
// it will.
var ErrTooManyStreams = errors.New("this controller already has as many camera streams open as it will (a stream stays open 30 seconds after its last viewer leaves): try again shortly")

// NewHLSHub makes a hub that opens cameras through f (its credentials). Their
// streams' memory is counted in budget (DefaultLiveBudget when nil).
func NewHLSHub(f *Fetcher, budget *MemBudget) *HLSHub {
	if budget == nil {
		budget = NewMemBudget(0)
	}
	return &HLSHub{f: f, budget: budget, sessions: map[string]*HLSSession{}, failed: map[string]hlsFailure{}}
}

// Budget is the memory the hub's streams share.
func (h *HLSHub) Budget() *MemBudget { return h.budget }

// Session returns camera c's session, starting it if there is none or the last one
// ended, and marks it used.
func (h *HLSHub) Session(c Camera) (*HLSSession, error) {
	if !c.HLS() {
		return nil, errors.New("camera: not an RTSP camera")
	}
	if c.Kind == KindONVIF {
		// a stream that is playing is not held up by a device slow to answer again: only a
		// camera with no live session asks the device (its answer is kept for a while, and a
		// device that is down is not asked again at once)
		if s := h.live(c.ID); s != nil {
			return s, nil
		}
		var err error
		if c, err = h.f.Resolve(context.Background(), c); err != nil {
			return nil, err
		}
	}
	user, pass, err := h.f.credentials(c)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.failed[c.ID]; ok {
		if time.Now().Before(f.until) {
			return nil, f.err
		}
		delete(h.failed, c.ID)
	}
	if s := h.sessions[c.ID]; s != nil {
		select {
		case <-s.done: // ended (the camera stopped or failed): the next request starts afresh
			delete(h.sessions, c.ID)
		default:
			s.last = time.Now()
			return s, nil
		}
	}
	// sessions linger for HLSIdle after their last viewer: the number open is capped,
	// so one person walking every camera cannot hold every camera open
	if len(h.sessions) >= MaxLive {
		return nil, ErrTooManyStreams
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &HLSSession{Stream: NewHLSStream(h.budget), cancel: cancel, done: make(chan struct{}), last: time.Now()}
	h.sessions[c.ID] = s
	go func() {
		defer close(s.done)
		// what a camera sends must never take the controller down: a panic in the
		// pipeline ends this stream, not the process
		defer func() {
			if r := recover(); r != nil {
				slog.Error("camera: an RTSP session panicked", "camera", c.ID, "panic", r)
				err := errors.New("the camera sent something the controller could not relay")
				s.Stream.Close(err)
				h.mu.Lock()
				h.failed[c.ID] = hlsFailure{err: err, until: time.Now().Add(HLSRetry)}
				h.mu.Unlock()
			}
		}()
		err := runRTSPHLS(ctx, c.URL, user, pass, s.Stream)
		s.Stream.Close(err)
		if err != nil && ctx.Err() == nil {
			h.mu.Lock()
			h.failed[c.ID] = hlsFailure{err: err, until: time.Now().Add(HLSRetry)}
			h.mu.Unlock()
		}
	}()
	if !h.reaping {
		h.reaping = true
		go h.reap()
	}
	return s, nil
}

// live returns camera id's session if it is running, marked used.
func (h *HLSHub) live(id string) *HLSSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	default:
		s.last = time.Now()
		return s
	}
}

// reap stops the sessions nobody has asked for lately, until there are none.
func (h *HLSHub) reap() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		h.mu.Lock()
		for id, s := range h.sessions {
			select {
			case <-s.done:
				delete(h.sessions, id)
				continue
			default:
			}
			if time.Since(s.last) > HLSIdle {
				s.cancel()
				delete(h.sessions, id)
			}
		}
		if len(h.sessions) == 0 {
			h.reaping = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
	}
}

// Stop ends every session.
func (h *HLSHub) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.sessions {
		s.cancel()
		delete(h.sessions, id)
	}
}

// Active is how many sessions run.
func (h *HLSHub) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// runRTSPHLS is the pipeline: the camera's H.264 from RTSP into the stream, until
// ctx ends, the camera stops or something it sends cannot be relayed. What it
// returns names neither the camera nor a credential.
func runRTSPHLS(ctx context.Context, raw, user, pass string, st *HLSStream) error {
	// a stream refused its memory at birth never dials the camera
	if err := st.Err(); err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, err := dialRTSP(dctx, raw, user, pass)
	if err != nil {
		cancel()
		return err
	}
	defer c.close()
	m, err := c.describe(dctx)
	if err == nil {
		err = c.setup(dctx, m)
	}
	if err == nil {
		err = c.play(dctx)
	}
	cancel()
	if err != nil {
		return err
	}
	defer c.teardown()
	d := &Depacketizer{SPS: m.SPS, PPS: m.PPS}
	if len(m.SPS) > 0 && len(m.PPS) > 0 {
		if err := st.SetParams(m.SPS, m.PPS); err != nil {
			return err
		}
	}
	var perr error // a parameter set that cannot be relayed ends the session
	emit := func(au AccessUnit) {
		if len(d.SPS) > 0 && len(d.PPS) > 0 {
			if err := st.SetParams(d.SPS, d.PPS); err != nil {
				perr = err
				return
			}
		}
		st.Feed(au)
	}
	err = c.run(ctx, StreamIdle, func(p RTPPacket) error {
		if int(p.PT) != m.PayloadType {
			return nil
		}
		if err := d.Push(p, emit); err != nil {
			return nil // a malformed packet is a loss
		}
		if perr != nil {
			return perr
		}
		return st.Err()
	})
	if err == nil && ctx.Err() == nil {
		err = errors.New("the camera stopped")
	}
	return err
}
