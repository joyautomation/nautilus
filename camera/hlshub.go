//go:build !js

package camera

import (
	"context"
	"errors"
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
	mu       sync.Mutex
	sessions map[string]*HLSSession
	reaping  bool
}

// NewHLSHub makes a hub that opens cameras through f (its credentials).
func NewHLSHub(f *Fetcher) *HLSHub { return &HLSHub{f: f, sessions: map[string]*HLSSession{}} }

// Session returns camera c's session, starting it if there is none or the last one
// ended, and marks it used.
func (h *HLSHub) Session(c Camera) (*HLSSession, error) {
	if c.Kind != KindRTSP {
		return nil, errors.New("camera: not an RTSP camera")
	}
	user, pass, err := h.f.credentials(c)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[c.ID]; s != nil {
		select {
		case <-s.done: // ended (the camera stopped or failed): the next request starts afresh
			delete(h.sessions, c.ID)
		default:
			s.last = time.Now()
			return s, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &HLSSession{Stream: NewHLSStream(), cancel: cancel, done: make(chan struct{}), last: time.Now()}
	h.sessions[c.ID] = s
	go func() {
		defer close(s.done)
		err := runRTSPHLS(ctx, c.URL, user, pass, s.Stream)
		s.Stream.Close(err)
	}()
	if !h.reaping {
		h.reaping = true
		go h.reap()
	}
	return s, nil
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
