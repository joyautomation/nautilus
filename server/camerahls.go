//go:build !js

package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/camera"
)

// Live video of an RTSP camera, as HLS (RFC 8216) in fragmented MP4 (#724 slice 3):
//
//	GET /api/cameras/{id}/hls/index.m3u8   the playlist (waits for the first segments)
//	GET /api/cameras/{id}/hls/init.mp4     the init segment
//	GET /api/cameras/{id}/hls/seg-N.m4s    a segment
//
// The controller opens the camera over RTSP when the first person asks, remuxes
// its H.264 without decoding it, and closes the camera 30 s after the last
// request. A person watching is a lease on the camera, kept alive by their
// requests and dropped after hlsLeaseTTL of silence: the per-camera, per-person and
// controller-wide caps count leases, and a live view is journalled when its lease
// begins and ends, as a motion-JPEG one is. Every request checks the person's
// right to read and to view cameras afresh, so a revoked person is refused at
// their next request, a segment's length at most. A lease ends after the
// maxLiveView time limit too.

// hlsLeaseTTL is how long a viewer's lease outlives their last request.
var hlsLeaseTTL = 15 * time.Second

type hlsLease struct {
	release  func()
	who      string
	camera   camera.Camera
	started  time.Time
	until    time.Time
	bytes    int64
	from     string
	session  string
	endedWhy string
}

// leaseFor refreshes (or takes) the request's person's lease on c. It reports
// false, having answered, when a cap is reached or the view's time is up.
func (a *cameraAPI) leaseFor(w http.ResponseWriter, r *http.Request, c camera.Camera) (*hlsLease, bool) {
	who := a.s.principalOf(r)
	holder := who
	if who == "anonymous" {
		holder = who + "@" + a.s.clientAddr(r)
	}
	key := holder + "|" + c.ID
	now := time.Now()
	co := a.co
	co.leaseMu.Lock()
	if co.leases == nil {
		co.leases = map[string]*hlsLease{}
	}
	if l := co.leases[key]; l != nil {
		if now.Sub(l.started) > maxLiveView {
			l.endedWhy = "the live view reached its time limit"
			co.endLeaseLocked(a, key, l)
			co.leaseMu.Unlock()
			writeJSON(w, http.StatusGone, map[string]string{"error": "the live view reached its time limit: start it again"})
			return nil, false
		}
		l.until = now.Add(hlsLeaseTTL)
		co.leaseMu.Unlock()
		return l, true
	}
	release, err := co.limit.Acquire(c, holder)
	if err != nil {
		co.leaseMu.Unlock()
		code := http.StatusServiceUnavailable
		if errors.Is(err, camera.ErrBusy) {
			code = http.StatusTooManyRequests
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return nil, false
	}
	l := &hlsLease{release: release, who: who, camera: c, started: now, until: now.Add(hlsLeaseTTL), from: a.s.clientAddr(r), session: a.s.sessionOfEntry(r)}
	co.leases[key] = l
	if !co.leasing {
		co.leasing = true
		go co.reapLeases(a)
	}
	co.leaseMu.Unlock()
	a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultOK, From: l.from, Session: l.session,
		Detail: map[string]any{"event": "start", "asset": c.Asset, "protocol": "hls"}})
	return l, true
}

// dropLease gives a lease back at once (the camera could not be opened).
func (a *cameraAPI) dropLease(r *http.Request, c camera.Camera, why string) {
	who := a.s.principalOf(r)
	holder := who
	if who == "anonymous" {
		holder = who + "@" + a.s.clientAddr(r)
	}
	key := holder + "|" + c.ID
	a.co.leaseMu.Lock()
	defer a.co.leaseMu.Unlock()
	if l := a.co.leases[key]; l != nil {
		l.endedWhy = why
		a.co.endLeaseLocked(a, key, l)
	}
}

// endLeaseLocked releases a lease and journals the end of its view.
func (co *CameraOptions) endLeaseLocked(a *cameraAPI, key string, l *hlsLease) {
	delete(co.leases, key)
	l.release()
	d := map[string]any{"event": "stop", "asset": l.camera.Asset, "protocol": "hls", "seconds": int(time.Since(l.started).Seconds()), "bytes": l.bytes}
	if l.endedWhy != "" {
		d["ended"] = l.endedWhy
	}
	a.s.audit.record(audit.Entry{Action: "camera.view", Target: l.camera.ID, User: l.who, Source: audit.SourceHMI, Result: audit.ResultOK, From: l.from, Session: l.session, Detail: d})
}

// reapLeases ends the leases nobody has asked to keep, until there are none.
func (co *CameraOptions) reapLeases(a *cameraAPI) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		co.leaseMu.Lock()
		now := time.Now()
		for k, l := range co.leases {
			if now.After(l.until) {
				co.endLeaseLocked(a, k, l)
			}
		}
		if len(co.leases) == 0 {
			co.leasing = false
			co.leaseMu.Unlock()
			return
		}
		co.leaseMu.Unlock()
	}
}

func (a *cameraAPI) hls(w http.ResponseWriter, r *http.Request) {
	c, ok := a.camera(w, r)
	if !ok {
		return
	}
	if !c.HLS() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this camera has no HLS stream"})
		return
	}
	name := r.PathValue("name")
	seq, isSeg := camera.ParseSegName(name)
	if name != "index.m3u8" && name != "init.mp4" && !isSeg {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such file"})
		return
	}
	l, ok := a.leaseFor(w, r, c)
	if !ok {
		return
	}
	sess, err := a.co.hub.Session(c)
	if err != nil {
		a.dropLease(r, c, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if name == "index.m3u8" {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		if err := sess.Stream.Wait(ctx); err != nil {
			a.dropLease(r, c, err.Error())
			code := http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) {
				err = errors.New("the camera sent no picture in time")
				code = http.StatusGatewayTimeout
			} else if errors.Is(err, context.Canceled) {
				return
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		pl := sess.Stream.Playlist()
		if pl == "" {
			a.dropLease(r, c, "the stream ended")
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the camera's stream ended"})
			return
		}
		a.hlsReply(w, l, "application/vnd.apple.mpegurl", []byte(pl))
		return
	}
	var body []byte
	if name == "init.mp4" {
		body = sess.Stream.Init()
		if body == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "the stream has not started"})
			return
		}
	} else {
		b, ok := sess.Stream.Segment(seq)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "that segment is not (or no longer) in the window"})
			return
		}
		body = b
	}
	a.hlsReply(w, l, "video/mp4", body)
}

func (a *cameraAPI) hlsReply(w http.ResponseWriter, l *hlsLease, ct string, body []byte) {
	pictureHeaders(w, ct)
	w.Header().Set("Content-Length", itoaInt(len(body)))
	a.co.leaseMu.Lock()
	l.bytes += int64(len(body))
	a.co.leaseMu.Unlock()
	w.Write(body)
}

func itoaInt(n int) string {
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
