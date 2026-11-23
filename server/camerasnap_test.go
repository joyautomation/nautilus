package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/camera"
)

func snapDir(t *testing.T, free func(string) (uint64, uint64, bool)) *camera.SnapStore {
	t.Helper()
	if free == nil {
		free = func(string) (uint64, uint64, bool) { return 0, 0, false }
	}
	st, err := camera.OpenSnapStore(filepath.Join(t.TempDir(), "snapshots"), camera.SnapOptions{Free: free})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// snapServer is a server with auth and cameras that keep pictures for alarms: a
// virtual camera on asset P101, a snapshot camera behind a backend on TK101, and
// one that names an alarm id of any equipment.
func snapServer(t *testing.T, backend string, st *camera.SnapStore, leads func() bool) (*Server, map[string]string, *CameraOptions) {
	t.Helper()
	co := &CameraOptions{
		Cameras: []camera.Camera{
			{ID: "pump-cam", Asset: "P101", Kind: camera.KindVirtual, SnapshotOnAlarm: true},
			{ID: "tank-cam", Asset: "TK101", Kind: camera.KindSnapshot, URL: backend, SnapshotAlarms: []string{"PlantTrip"}},
		},
		Fetcher:   &camera.Fetcher{},
		Snaps:     st,
		Equipment: func(id string) string { return map[string]string{"PumpFault": "p101", "TankHigh": "TK101"}[id] },
		Leads:     leads,
	}
	o := Options{}
	co.Install(&o)
	srv, toks, _ := patServerSettings(t, newTestRuntime(t), o, camSettings())
	srv.Handler() // mounts the routes: the notifier needs the server's journal
	return srv, toks, co
}

func active(id, occ string, ts int64) alarm.Event {
	return alarm.Event{ID: id, Occurrence: occ, TS: ts, Kind: alarm.KindActive}
}

func waitSnaps(t *testing.T, st *camera.SnapStore, occ string, want int) []camera.Snap {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := st.ForOccurrence(occ); len(got) == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("occurrence %s: %d pictures, want %d", occ, len(st.ForOccurrence(occ)), want)
	return nil
}

func TestAnAlarmActivationKeepsTheStillOfTheCamerasThatWatchItsEquipment(t *testing.T) {
	var hits atomic.Int64
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(camJPEG(t)) }))
	defer back.Close()
	st := snapDir(t, nil)
	srv, toks, co := snapServer(t, back.URL, st, nil)
	n := co.AlarmNotifier()
	now := time.Now().UnixMilli()

	// the pump's equipment (a different case than the camera's asset): the virtual camera
	n.Notify(context.Background(), active("PumpFault", "occ-pump", now))
	got := waitSnaps(t, st, "occ-pump", 1)
	if got[0].Camera != "pump-cam" || got[0].Alarm != "PumpFault" {
		t.Errorf("the pump's picture: %+v", got[0])
	}
	// an alarm the tank camera names by id, of other equipment
	n.Notify(context.Background(), active("PlantTrip", "occ-trip", now))
	if g := waitSnaps(t, st, "occ-trip", 1); g[0].Camera != "tank-cam" || hits.Load() != 1 {
		t.Errorf("the named alarm: %+v (camera asked %d times)", g, hits.Load())
	}
	// not a new activation, no occurrence, or equipment no camera watches: nothing
	standing := active("PumpFault", "occ-standing", now+10000)
	standing.Standing = true
	n.Notify(context.Background(), standing)
	n.Notify(context.Background(), active("PumpFault", "", now+10000))
	n.Notify(context.Background(), alarm.Event{ID: "PumpFault", Occurrence: "occ-ack", TS: now + 10000, Kind: alarm.KindAck})
	n.Notify(context.Background(), active("Elsewhere", "occ-else", now+10000))
	time.Sleep(150 * time.Millisecond)
	for _, occ := range []string{"occ-standing", "occ-ack", "occ-else"} {
		if len(st.ForOccurrence(occ)) != 0 {
			t.Errorf("a picture was kept for %s", occ)
		}
	}
	// each capture is journalled by the system
	found := 0
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.snapshot" && e.User == "system" && e.Result == "ok" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("%d camera.snapshot entries, want 2", found)
	}
	// a camera that did not answer is journalled as an error and keeps nothing
	back.Close()
	n.Notify(context.Background(), active("PlantTrip", "occ-down", now+20000))
	deadline := time.Now().Add(5 * time.Second)
	errored := false
	for time.Now().Before(deadline) && !errored {
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "camera.snapshot" && e.Result == "error" && e.Detail["occurrence"] == "occ-down" {
				errored = !strings.Contains(strings.Join([]string{e.Detail["error"].(string)}, ""), "127.0.0.1")
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !errored || len(st.ForOccurrence("occ-down")) != 0 {
		t.Errorf("a camera that did not answer: journalled error %v, pictures %d", errored, len(st.ForOccurrence("occ-down")))
	}
	_ = toks
}

// A flood shares one picture per camera inside the window, a standby captures
// nothing, and the captures in flight are bounded.
func TestAnAlarmFloodSharesAPictureAndAStandbyKeepsNone(t *testing.T) {
	st := snapDir(t, nil)
	lead := atomic.Bool{}
	lead.Store(false)
	_, _, co := snapServer(t, "http://127.0.0.1:1/x", st, lead.Load)
	n := co.AlarmNotifier()
	now := time.Now().UnixMilli()
	n.Notify(context.Background(), active("PumpFault", "occ-s", now))
	time.Sleep(150 * time.Millisecond)
	if len(st.ForOccurrence("occ-s")) != 0 {
		t.Error("a standby kept a picture")
	}
	lead.Store(true)
	n.Notify(context.Background(), active("PumpFault", "occ-1", now))
	waitSnaps(t, st, "occ-1", 1)
	for i, occ := range []string{"occ-2", "occ-3", "occ-4"} {
		n.Notify(context.Background(), active("PumpFault", occ, now+int64(100*(i+1))))
		waitSnaps(t, st, occ, 1)
	}
	if st.Len() != 1 {
		t.Errorf("%d pictures kept for a flood inside the window, want 1 shared", st.Len())
	}
	// outside the window a new picture
	n.Notify(context.Background(), active("PumpFault", "occ-late", now+60000))
	waitSnaps(t, st, "occ-late", 1)
	if st.Len() != 2 {
		t.Errorf("%d pictures after one outside the window, want 2", st.Len())
	}
}

// Seeing a kept picture needs the camera permission; a name this store did not
// make is a 404; purging needs config and is journalled.
func TestKeptPicturesNeedThePermissionAndPurgeIsAudited(t *testing.T) {
	st := snapDir(t, nil)
	srv, toks, co := snapServer(t, "http://127.0.0.1:1/x", st, nil)
	now := time.Now().UnixMilli()
	co.AlarmNotifier().Notify(context.Background(), active("PumpFault", "occ-1", now))
	sn := waitSnaps(t, st, "occ-1", 1)[0]
	eng, blind := bearer(toks["akhan"]), bearer(toks["jsmith"])

	rec := call(t, srv, "GET", "/api/camera-snapshots?occurrence=occ-1", "", eng)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), sn.File) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/camera-snapshots/"+sn.File, "", eng); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("a picture: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/api/camera-snapshots?occurrence=occ-1", "/api/camera-snapshots/" + sn.File} {
		if rec := call(t, srv, "GET", p, "", blind); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no permission to view cameras") {
			t.Errorf("GET %s without the camera permission: %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"/api/camera-snapshots/..%2F..%2Fetc%2Fpasswd", "/api/camera-snapshots/index.jsonl", "/api/camera-snapshots/17000000000-nope.jpg"} {
		if rec := call(t, srv, "GET", p, "", eng); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", p, rec.Code)
		}
	}
	// purge: a person without config is refused and nothing goes
	if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", `{"all":true}`, blind); rec.Code != http.StatusForbidden || st.Len() != 1 {
		t.Errorf("a purge by an operator: %d, %d kept", rec.Code, st.Len())
	}
	if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", `{}`, eng); rec.Code != http.StatusBadRequest {
		t.Errorf("a purge that says nothing: %d", rec.Code)
	}
	if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", `{"all":true,"olderThanDays":3}`, eng); rec.Code != http.StatusBadRequest {
		t.Errorf("a purge that says two things: %d", rec.Code)
	}
	if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", `{"all":true}`, eng); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"purged":1`) || st.Len() != 0 {
		t.Errorf("the purge: %d %s, %d kept", rec.Code, rec.Body, st.Len())
	}
	purged := false
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.snapshot-purge" && e.Result == "ok" && e.User == "user:akhan" {
			purged = true
		}
	}
	if !purged {
		t.Error("the purge is not journalled")
	}
	if rec := call(t, srv, "GET", "/api/camera-snapshots/"+sn.File, "", eng); rec.Code != http.StatusNotFound {
		t.Errorf("a purged picture: %d", rec.Code)
	}
}

// With the disk under the floor no picture is kept, and the capture says why.
func TestNoPicturesAreKeptWhileTheDiskIsNearlyFull(t *testing.T) {
	var free atomic.Uint64
	free.Store(1 << 30)
	st := snapDir(t, func(string) (uint64, uint64, bool) { return free.Load(), 8 << 30, true })
	srv, _, co := snapServer(t, "http://127.0.0.1:1/x", st, nil)
	n := co.AlarmNotifier()
	now := time.Now().UnixMilli()
	n.Notify(context.Background(), active("PumpFault", "occ-ok", now))
	waitSnaps(t, st, "occ-ok", 1)
	free.Store(50 << 20)
	n.Notify(context.Background(), active("PumpFault", "occ-full", now+60000))
	deadline := time.Now().Add(5 * time.Second)
	said := false
	for time.Now().Before(deadline) && !said {
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "camera.snapshot" && e.Result == "error" && strings.Contains(e.Detail["error"].(string), "nearly full") {
				said = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !said || len(st.ForOccurrence("occ-full")) != 0 {
		t.Errorf("under the floor: journalled %v, pictures %d", said, len(st.ForOccurrence("occ-full")))
	}
}
