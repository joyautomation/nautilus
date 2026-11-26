package server

import (
	"context"
	"fmt"
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

// waitIdle waits until no fetch is running for any camera: the picture is stored before the
// fetch's bookkeeping ends, and an event that arrives in between joins that fetch and gets
// its picture whatever its own time is (the design: a flood shares one), which is not what a
// test that wants a fresh capture means to ask.
func waitIdle(t *testing.T, co *CameraOptions) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		co.mu.Lock()
		n := len(co.inflight)
		co.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a fetch was still running after 5 s")
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
	// looking at a kept picture is journalled once per person and picture, with its occurrences
	views := func() (n int, who, occ string) {
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "camera.snapshot-view" && e.Target == sn.File {
				n++
				who, occ = e.User, fmt.Sprint(e.Detail["occurrences"])
			}
		}
		return
	}
	if n, who, occ := views(); n != 1 || who != "user:akhan" || !strings.Contains(occ, "occ-1") {
		t.Errorf("journalled views of the picture: %d by %q for %s", n, who, occ)
	}
	call(t, srv, "GET", "/api/camera-snapshots/"+sn.File, "", eng)
	call(t, srv, "GET", "/api/camera-snapshots/"+sn.File, "", eng)
	if n, _, _ := views(); n != 1 {
		t.Errorf("a reload wrote %d view rows for one person and picture, want 1", n)
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
	waitIdle(t, co) // the next event must find no fetch to join: it needs a fresh write, which the floor refuses
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

// A real flood: many activations arrive while the first fetch is still running.
// Every one of them ends up with the picture (one fetch), none is skipped.
func TestAFloodThatArrivesWhileTheFirstFetchRunsAllGetThePicture(t *testing.T) {
	var hits atomic.Int64
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(400 * time.Millisecond)
		w.Write(camJPEG(t))
	}))
	defer back.Close()
	st := snapDir(t, nil)
	srv, _, co := snapServer(t, back.URL, st, nil)
	n := co.AlarmNotifier()
	now := time.Now().UnixMilli()
	const flood = 12
	for i := 0; i < flood; i++ {
		n.Notify(context.Background(), active("PlantTrip", fmt.Sprintf("occ-%d", i), now+int64(i)))
	}
	for i := 0; i < flood; i++ {
		waitSnaps(t, st, fmt.Sprintf("occ-%d", i), 1)
	}
	if hits.Load() != 1 || st.Len() != 1 {
		t.Errorf("a flood of %d: the camera was asked %d times and %d pictures were kept, want 1 and 1", flood, hits.Load(), st.Len())
	}
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.snapshot" && e.Result == "error" {
			t.Errorf("an activation in the flood was journalled as failed: %+v", e.Detail)
		}
	}
}

// With every capture place taken by other cameras, an activation is journalled
// as skipped: not lost without a trace.
func TestAnActivationThatFindsTheCapturePlacesBusyIsJournalled(t *testing.T) {
	release := make(chan struct{})
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release; w.Write(camJPEG(t)) }))
	defer back.Close()
	st := snapDir(t, nil)
	// let the two running captures finish before the temp directory goes
	defer func() {
		close(release)
		for i := 0; i < 100 && st.Len() < 2; i++ {
			time.Sleep(30 * time.Millisecond)
		}
	}()
	co := &CameraOptions{
		Cameras: []camera.Camera{
			{ID: "c1", Asset: "E1", Kind: camera.KindSnapshot, URL: back.URL, SnapshotOnAlarm: true},
			{ID: "c2", Asset: "E2", Kind: camera.KindSnapshot, URL: back.URL, SnapshotOnAlarm: true},
			{ID: "c3", Asset: "E3", Kind: camera.KindSnapshot, URL: back.URL, SnapshotOnAlarm: true},
		},
		Fetcher: &camera.Fetcher{}, Snaps: st,
		Equipment: func(id string) string { return id },
	}
	o := Options{}
	co.Install(&o)
	srv, _, _ := patServerSettings(t, newTestRuntime(t), o, camSettings())
	srv.Handler()
	n := co.AlarmNotifier()
	now := time.Now().UnixMilli()
	for i, id := range []string{"E1", "E2", "E3"} {
		n.Notify(context.Background(), active(id, "occ-"+id, now+int64(i)))
	}
	deadline := time.Now().Add(3 * time.Second)
	skipped := false
	for time.Now().Before(deadline) && !skipped {
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "camera.snapshot" && e.Result == "error" && strings.Contains(fmt.Sprint(e.Detail["error"]), "busy") && e.Target == "c3" {
				skipped = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !skipped {
		t.Error("the third camera's activation, with both capture places taken, is not journalled")
	}
}

func TestPurgeRefusesDaysOutOfRange(t *testing.T) {
	st := snapDir(t, nil)
	srv, toks, _ := snapServer(t, "http://127.0.0.1:1/x", st, nil)
	eng := bearer(toks["akhan"])
	for _, body := range []string{`{"olderThanDays":106752}`, `{"olderThanDays":-3}`, `{"olderThanDays":3651}`} {
		if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", body, eng); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	if rec := call(t, srv, "POST", "/api/camera-snapshots/purge", `{"olderThanDays":30}`, eng); rec.Code != 200 {
		t.Errorf("a month: %d", rec.Code)
	}
}
