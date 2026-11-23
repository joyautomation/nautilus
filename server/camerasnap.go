//go:build !js

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/camera"
)

// Pictures kept when an alarm activates (#724 slice 2):
//
//	GET  /api/camera-snapshots?occurrence=ID   the pictures kept for an alarm occurrence
//	GET  /api/camera-snapshots/{file}          one picture (image/jpeg)
//	POST /api/camera-snapshots/purge           {olderThanDays?, all?}: drop pictures (config permission, audited)
//
// A picture is attached to an alarm occurrence by that key, not by a change to
// the journal's rows. Seeing one needs read and the camera permission, as a live
// view does; the picture's name is the only thing a request chooses, and a name
// this store did not make never reaches the filesystem. A capture is journalled
// (camera.snapshot, by system). Only the controller that leads captures; after a
// takeover the older pictures stay on the previous leader's box.

const (
	// snapWindowMs: an alarm flood shares one picture per camera inside this window.
	snapWindowMs = 5000
	// snapParallel bounds the captures in flight: the engine's queue never waits on a camera.
	snapParallel = 2
	snapTimeout  = 12 * time.Second
)

// AlarmNotifier is what hears the alarm engine and captures: a new activation
// (not a standing one) of an alarm whose equipment is a camera's asset, or that
// a camera names, keeps a still. It runs off the engine's queue goroutine
// (captures are bounded and dropped when busy) and only on the controller that
// leads.
func (co *CameraOptions) AlarmNotifier() alarm.Notifier { return camSnapNotifier{co} }

type camSnapNotifier struct{ co *CameraOptions }

func (n camSnapNotifier) Notify(_ context.Context, ev alarm.Event) error {
	co := n.co
	if co.Snaps == nil || ev.Kind != alarm.KindActive || ev.Standing || ev.Occurrence == "" {
		return nil
	}
	if co.Leads != nil && !co.Leads() {
		return nil
	}
	eq := ""
	if co.Equipment != nil {
		eq = co.Equipment(ev.ID)
	}
	for _, c := range co.Cameras {
		want := false
		if c.SnapshotOnAlarm && eq != "" && strings.EqualFold(eq, c.Asset) {
			want = true
		}
		for _, id := range c.SnapshotAlarms {
			if id == ev.ID {
				want = true
			}
		}
		if !want {
			continue
		}
		co.want(c, ev)
	}
	return nil
}

// want asks for camera c's picture for ev. A picture the camera took within the
// window is shared at once (a flood costs no fetch); a fetch already running for
// the camera takes the event along (it is attached when the picture lands); only
// a new fetch needs one of the two capture places, and an event that finds them
// all busy is journalled as skipped, not lost without a trace.
func (co *CameraOptions) want(c camera.Camera, ev alarm.Event) {
	if sn, ok := co.Snaps.Recent(c.ID, ev.TS, snapWindowMs); ok {
		co.attach(c, ev, sn)
		return
	}
	co.mu.Lock()
	if co.inflight == nil {
		co.inflight = map[string][]alarm.Event{}
	}
	if q, running := co.inflight[c.ID]; running {
		co.inflight[c.ID] = append(q, ev)
		co.mu.Unlock()
		return
	}
	co.semOnce.Do(func() { co.snapSem = make(chan struct{}, snapParallel) })
	select {
	case co.snapSem <- struct{}{}:
		co.inflight[c.ID] = []alarm.Event{ev}
		co.mu.Unlock()
		go func() {
			defer func() { <-co.snapSem }()
			co.capture(c)
		}()
	default:
		co.mu.Unlock()
		slog.Warn("camera: an alarm picture was skipped: captures are busy", "camera", c.ID, "alarm", ev.ID)
		co.record(c, ev, audit.ResultError, map[string]any{"error": "captures are busy: no picture was taken for this activation"})
	}
}

// record journals a capture's outcome by the system.
func (co *CameraOptions) record(c camera.Camera, ev alarm.Event, result string, d map[string]any) {
	co.mu.Lock()
	api := co.api
	co.mu.Unlock()
	if api == nil {
		slog.Warn("camera: an alarm picture was skipped: the server is not up yet", "camera", c.ID, "alarm", ev.ID)
		return
	}
	d["asset"] = c.Asset
	d["alarm"] = ev.ID
	d["occurrence"] = ev.Occurrence
	api.s.audit.record(audit.Entry{Action: "camera.snapshot", Target: c.ID, User: "system", Source: audit.SourceSystem, Result: result, Detail: d})
}

// attach shares picture sn with the event's occurrence.
func (co *CameraOptions) attach(c camera.Camera, ev alarm.Event, sn camera.Snap) {
	if err := co.Snaps.Attach(sn, ev.ID, ev.Occurrence); err != nil {
		co.record(c, ev, audit.ResultError, map[string]any{"error": err.Error()})
		return
	}
	co.record(c, ev, audit.ResultOK, map[string]any{"file": sn.File, "shared": true})
}

// capture fetches and keeps camera c's still for the event that started it, then
// shares the picture with every event that arrived while it ran.
func (co *CameraOptions) capture(c camera.Camera) {
	co.mu.Lock()
	evs := co.inflight[c.ID]
	co.mu.Unlock()
	finish := func() []alarm.Event {
		co.mu.Lock()
		defer co.mu.Unlock()
		more := co.inflight[c.ID]
		delete(co.inflight, c.ID)
		return more
	}
	if len(evs) == 0 {
		finish()
		return
	}
	first := evs[0]
	fail := func(err error) {
		for _, ev := range finish() {
			co.record(c, ev, audit.ResultError, map[string]any{"error": err.Error()})
		}
	}
	if co.Fetcher == nil {
		fail(errors.New("no camera fetcher"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapTimeout)
	defer cancel()
	b, err := co.Fetcher.Snapshot(ctx, c)
	if err != nil {
		fail(err)
		return
	}
	sn, err := co.Snaps.Save(first.TS, c.ID, first.ID, first.Occurrence, b)
	if err != nil {
		if errors.Is(err, camera.ErrLowSpace) {
			if !co.lowWarned.Swap(true) {
				slog.Warn("camera: the state filesystem is nearly full: alarm pictures are no longer kept until space returns")
			}
		}
		fail(err)
		return
	}
	co.lowWarned.Store(false)
	// the picture is in: the first event owns it, the others that waited share it
	all := finish()
	co.record(c, first, audit.ResultOK, map[string]any{"file": sn.File, "bytes": sn.Size})
	for _, ev := range all[1:] {
		co.attach(c, ev, sn)
	}
}

func (a *cameraAPI) mountSnaps(mux *http.ServeMux) {
	if a.co.Snaps == nil {
		return
	}
	mux.HandleFunc("GET /api/camera-snapshots", a.snapList)
	mux.HandleFunc("GET /api/camera-snapshots/{file}", a.snapFile)
	mux.Handle("POST /api/camera-snapshots/purge", a.s.audited("camera.snapshot-purge", a.purgeTarget, a.snapPurge))
}

type snapView struct {
	File   string `json:"file"`
	TS     int64  `json:"ts"`
	Camera string `json:"camera"`
	Alarm  string `json:"alarm"`
	URL    string `json:"url"`
}

func (a *cameraAPI) snapList(w http.ResponseWriter, r *http.Request) {
	if !a.mayView(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": noView})
		return
	}
	out := []snapView{}
	for _, sn := range a.co.Snaps.ForOccurrence(r.URL.Query().Get("occurrence")) {
		out = append(out, snapView{File: sn.File, TS: sn.TS, Camera: sn.Camera, Alarm: sn.Alarm, URL: "/api/camera-snapshots/" + sn.File})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (a *cameraAPI) snapFile(w http.ResponseWriter, r *http.Request) {
	if !a.mayView(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": noView})
		return
	}
	b, err := a.co.Snaps.Read(r.PathValue("file"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such picture (it may be past its retention)"})
		return
	}
	pictureHeaders(w, "image/jpeg")
	w.Write(b)
}

func (a *cameraAPI) purgeTarget(r *http.Request, body []byte) auditTarget {
	var req purgeRequest
	_ = json.Unmarshal(body, &req)
	return auditTarget{target: "camera-snapshots", detail: map[string]any{"all": req.All, "olderThanDays": req.OlderThanDays},
		answered: func(b []byte) any {
			var r struct {
				Purged int `json:"purged"`
			}
			_ = json.Unmarshal(b, &r)
			return map[string]any{"purged": r.Purged}
		}}
}

type purgeRequest struct {
	OlderThanDays int  `json:"olderThanDays"`
	All           bool `json:"all"`
}

func (a *cameraAPI) snapPurge(w http.ResponseWriter, r *http.Request) {
	var req purgeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is JSON: {\"all\": true} or {\"olderThanDays\": N}"})
		return
	}
	if req.OlderThanDays < 0 || req.OlderThanDays > 3650 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "olderThanDays is between 1 and 3650"})
		return
	}
	if req.All == (req.OlderThanDays > 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "say what to purge: \"all\": true, or \"olderThanDays\": N (one of them)"})
		return
	}
	age := time.Duration(0)
	if !req.All {
		age = time.Duration(req.OlderThanDays) * 24 * time.Hour
	}
	n := a.co.Snaps.Purge(age)
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}
