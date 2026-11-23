//go:build !js

package camera

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateAcceptsGoodRowsAndNamesEachBadOne(t *testing.T) {
	good := []Camera{
		{ID: "bin-top", Asset: "BIN-3", Kind: KindVirtual},
		{ID: "dock", Asset: "D1", Kind: KindSnapshot, URL: "http://10.0.0.5/snap.jpg", Credentials: true, IntervalSeconds: 10},
		{ID: "belt", Asset: "C2", Kind: KindMJPEG, URL: "https://10.0.0.6/video", MaxViewers: 3},
		{ID: "r1", Asset: "C2", Kind: KindRTSP, URL: "rtsp://10.0.0.7/s2"},
	}
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("good rows refused: %v", errs)
	}
	for _, tc := range []struct {
		name string
		row  Camera
		want string
	}{
		{"no id", Camera{Asset: "A", Kind: KindVirtual}, "needs an id"},
		{"bad id", Camera{ID: "a b", Asset: "A", Kind: KindVirtual}, "letters, digits"},
		{"no asset", Camera{ID: "x", Kind: KindVirtual}, "needs an asset"},
		{"no kind", Camera{ID: "x", Asset: "A"}, "needs a kind"},
		{"unknown kind", Camera{ID: "x", Asset: "A", Kind: "thermal"}, `kind "thermal"`},
		{"mjpeg no url", Camera{ID: "x", Asset: "A", Kind: KindMJPEG}, "needs a url"},
		{"credentials in url", Camera{ID: "x", Asset: "A", Kind: KindMJPEG, URL: "http://admin:pw@10.0.0.1/v"}, "secrets file"},
		{"rtsp over http", Camera{ID: "x", Asset: "A", Kind: KindRTSP, URL: "http://10.0.0.1/v"}, "rtsp:// or rtsps://"},
		{"snapshot over rtsp", Camera{ID: "x", Asset: "A", Kind: KindSnapshot, URL: "rtsp://10.0.0.1/v"}, "http:// or https://"},
		{"interval", Camera{ID: "x", Asset: "A", Kind: KindVirtual, IntervalSeconds: 100000}, "interval"},
		{"viewers", Camera{ID: "x", Asset: "A", Kind: KindVirtual, MaxViewers: 99}, "max-viewers"},
	} {
		errs := Validate([]Camera{tc.row})
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.want) {
			t.Errorf("%s: %v, want it to say %q", tc.name, errs, tc.want)
		}
	}
	if errs := Validate([]Camera{{ID: "a", Asset: "A", Kind: KindVirtual}, {ID: "a", Asset: "A", Kind: KindVirtual}}); len(errs) == 0 || !strings.Contains(errs[0].Error(), "another camera") {
		t.Errorf("a duplicate id: %v", errs)
	}
	// a URL the error names must not carry the password it was refused for
	for _, e := range Validate([]Camera{{ID: "x", Asset: "A", Kind: KindMJPEG, URL: "http://admin:hunter2@10.0.0.1/v"}}) {
		if strings.Contains(e.Error(), "hunter2") {
			t.Errorf("the error repeats the password: %v", e)
		}
	}
}

func TestVirtualFrameIsAJPEGThatMoves(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	a := VirtualFrame("Bin top", []string{"Level 71.2 %"}, t0)
	b := VirtualFrame("Bin top", []string{"Level 71.2 %"}, t0.Add(2*time.Second))
	for _, f := range [][]byte{a, b} {
		img, err := jpeg.Decode(bytes.NewReader(f))
		if err != nil || img.Bounds().Dx() != VirtualW || img.Bounds().Dy() != VirtualH {
			t.Fatalf("not a %dx%d JPEG: %v", VirtualW, VirtualH, err)
		}
	}
	if bytes.Equal(a, b) {
		t.Error("two frames two seconds apart are the same: a frozen picture would look alive")
	}
}

func jpegBytes() []byte { return VirtualFrame("t", nil, time.Unix(0, 0)) }

func secrets(m map[string]string) func(string) (string, bool) {
	return func(n string) (string, bool) { v, ok := m[n]; return v, ok }
}

func TestSnapshotFetchesWithBasicAndDigestCredentials(t *testing.T) {
	jb := jpegBytes()
	basic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "viewer" || p != "pw" {
			w.Header().Set("Www-Authenticate", `Basic realm="cam"`)
			w.WriteHeader(401)
			return
		}
		w.Write(jb)
	}))
	defer basic.Close()
	digestSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if !strings.HasPrefix(a, "Digest ") {
			w.Header().Set("Www-Authenticate", `Digest realm="cam", nonce="abc123", qop="auth"`)
			w.WriteHeader(401)
			return
		}
		// recompute the response the way the client should have
		p := map[string]string{}
		for _, part := range splitParams(a[len("Digest "):]) {
			k, v, _ := strings.Cut(part, "=")
			p[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
		want := digestResponse("viewer", p["realm"], "pw", "GET", p["uri"], p["nonce"], p["nc"], p["cnonce"])
		if p["username"] != "viewer" || p["response"] != want {
			w.WriteHeader(401)
			return
		}
		w.Write(jb)
	}))
	defer digestSrv.Close()
	f := &Fetcher{Secret: secrets(map[string]string{"camera.cam": "viewer:pw", "camera.wrong": "viewer:nope"})}
	for name, srv := range map[string]*httptest.Server{"basic": basic, "digest": digestSrv} {
		c := Camera{ID: "cam", Asset: "A", Kind: KindSnapshot, URL: srv.URL + "/snap.jpg", Credentials: true}
		got, err := f.Snapshot(context.Background(), c)
		if err != nil || !bytes.Equal(got, jb) {
			t.Errorf("%s: %v", name, err)
		}
		c.ID = "wrong"
		if _, err := f.Snapshot(context.Background(), c); err == nil || !strings.Contains(err.Error(), "refused the controller's credentials") {
			t.Errorf("%s with a wrong password: %v", name, err)
		}
	}
}

func TestSnapshotRefusesWhatIsNotAStillAndNeverRepeatsCredentialsOrAddress(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, MaxSnapshotBytes+10)...))
	}))
	defer big.Close()
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>login</html>") }))
	defer html.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(jpegBytes()) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer redir.Close()
	// a camera that takes the connection and closes it without a word: a transport
	// error of the kind the address is in
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	f := &Fetcher{Secret: secrets(map[string]string{"camera.c": "u:topsecret"})}
	for name, tc := range map[string]struct{ url, want string }{
		"oversize":            {big.URL, "larger than"},
		"not a jpeg":          {html.URL, "did not return a JPEG"},
		"other-host redirect": {redir.URL, "another host"},
		"nothing listening":   {"http://127.0.0.1:1/x", "did not answer"},
		"closed unanswered":   {"http://" + ln.Addr().String() + "/x", "did not answer"},
	} {
		_, err := f.Snapshot(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindSnapshot, URL: tc.url, Credentials: true})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("%s: the error repeats a credential or the camera's address: %v", name, err)
		}
	}
	if _, err := f.Snapshot(context.Background(), Camera{ID: "m", Asset: "A", Kind: KindSnapshot, URL: html.URL, Credentials: true}); err == nil || !strings.Contains(err.Error(), `"camera.m" is not in the secrets file`) {
		t.Errorf("a missing secret: %v", err)
	}
	if _, err := f.Snapshot(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindRTSP, URL: "rtsp://10.0.0.1/s"}); err != ErrNoSnapshot {
		t.Errorf("an rtsp camera with no snapshot-url: %v", err)
	}
}

func mjpegServer(parts ...[]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := multipart.NewWriter(w)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mw.Boundary())
		for _, p := range parts {
			h := textproto.MIMEHeader{}
			h.Set("Content-Type", "image/jpeg")
			pw, _ := mw.CreatePart(h)
			pw.Write(p)
		}
		mw.Close()
	}))
}

func TestRelayCarriesFramesAndEndsOnAPartThatIsNotAJPEG(t *testing.T) {
	jb := jpegBytes()
	srv := mjpegServer(jb, jb, []byte("<html>not a frame</html>"), jb)
	defer srv.Close()
	f := &Fetcher{}
	var out bytes.Buffer
	var m Meter
	err := f.Relay(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindMJPEG, URL: srv.URL}, nil, &out, func() {}, &m)
	if err == nil || !strings.Contains(err.Error(), "not a JPEG") {
		t.Fatalf("relay ended with %v, want the non-JPEG part to end it", err)
	}
	if n := strings.Count(out.String(), "Content-Type: image/jpeg"); n != 2 {
		t.Errorf("%d frames reached the browser, want the 2 before the bad part", n)
	}
	if strings.Contains(out.String(), "not a frame") {
		t.Error("a part that is not a JPEG reached the browser")
	}
	if m.BitsPerSecond() < 0 || m.bytes.Load() < int64(2*len(jb)) {
		t.Errorf("the meter counted %d bytes", m.bytes.Load())
	}
	// a snapshot of an mjpeg camera is one of its frames
	got, err := f.Snapshot(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindMJPEG, URL: srv.URL})
	if err != nil || !bytes.Equal(got, jb) {
		t.Errorf("a frame from the stream: %v", err)
	}
	// a URL that is not motion-JPEG is said so
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") }))
	defer html.Close()
	if err := f.Relay(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindMJPEG, URL: html.URL}, nil, io.Discard, func() {}, nil); err == nil || !strings.Contains(err.Error(), "not a motion-JPEG stream") {
		t.Errorf("a URL that is not a stream: %v", err)
	}
}

func TestRelayOfAVirtualCameraRunsUntilTheViewerLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out lockedBuf
	done := make(chan error, 1)
	go func() {
		done <- (&Fetcher{}).Relay(ctx, Camera{ID: "v", Asset: "A", Kind: KindVirtual, Label: "v"}, func() []string { return []string{"level 1"} }, &out, func() {}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for strings.Count(out.String(), "image/jpeg") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("a viewer leaving ended the relay with %v", err)
	}
	if strings.Count(out.String(), "image/jpeg") < 2 {
		t.Error("the virtual camera sent fewer than two frames")
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestLimiterHoldsTheCameraCapTheControllersCapAndEachPersons(t *testing.T) {
	l := NewLimiter(3)
	a := Camera{ID: "a", MaxViewers: 2}
	b := Camera{ID: "b", MaxViewers: 2}
	r1, err := l.Acquire(a, "p1")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := l.Acquire(a, "p2")
	if _, err := l.Acquire(a, "p3"); err == nil || !strings.Contains(err.Error(), "max-viewers") {
		t.Errorf("a third viewer of a two-viewer camera: %v", err)
	}
	r3, err := l.Acquire(b, "p3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(b, "p4"); err == nil || !strings.Contains(err.Error(), "3 live streams") {
		t.Errorf("past the controller's cap: %v", err)
	}
	r1()
	r1() // giving back twice must not free a second place
	if l.Watching("a") != 1 {
		t.Errorf("watching a = %d after one release (twice called)", l.Watching("a"))
	}
	if _, err := l.Acquire(b, "p4"); err != nil {
		t.Errorf("a place was freed and is refused: %v", err)
	}
	r2()
	r3()
	// one person cannot take every place
	l = NewLimiter(8)
	c := Camera{ID: "c", MaxViewers: 8}
	for i := 0; i < MaxPerPerson; i++ {
		if _, err := l.Acquire(c, "same"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Acquire(c, "same"); err == nil || !strings.Contains(err.Error(), "already watching") {
		t.Errorf("a person past their own cap: %v", err)
	}
	if _, err := l.Acquire(c, "other"); err != nil {
		t.Errorf("another person was refused for the first one's places: %v", err)
	}
	if cm := (Camera{}); cm.Viewers() != DefaultMaxViewers || cm.Interval() != DefaultIntervalSeconds {
		t.Error("the defaults are not applied")
	}
}

func TestMeterIsTheAverageSinceTheFirstByte(t *testing.T) {
	var m Meter
	if m.BitsPerSecond() != 0 {
		t.Error("a meter with no bytes reports a rate")
	}
	m.Add(1000)
	time.Sleep(50 * time.Millisecond)
	m.Add(1000)
	if bps := m.BitsPerSecond(); bps < 100_000 || bps > 2_000_000 {
		t.Errorf("2000 bytes over ~50 ms is %d bit/s", bps)
	}
	m.Reset()
	if m.BitsPerSecond() != 0 {
		t.Error("reset did not clear the meter")
	}
}

// A transport or parser error carries the camera's address and its own bytes:
// whatever leaves the package is fixed words.
func TestErrorsFromABrokenCameraCarryNeitherItsAddressNorItsBytes(t *testing.T) {
	// a camera that promises a body and resets the connection
	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0})
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		rst(hj)
	}))
	defer reset.Close()
	f := &Fetcher{}
	_, err := f.Snapshot(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindSnapshot, URL: reset.URL})
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "tcp") {
		t.Errorf("a body cut short: %v", err)
	}
	// a part with a malformed header: the camera's own bytes must not be echoed
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=b")
		io.WriteString(w, "--b\r\nthis line has no colon SECRETTEXT\r\n\r\n")
	}))
	defer bad.Close()
	err = f.Relay(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindMJPEG, URL: bad.URL}, nil, io.Discard, func() {}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRETTEXT") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("a malformed part: %v", err)
	}
	// a stream cut in the middle of a frame
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=b")
		io.WriteString(w, "--b\r\nContent-Type: image/jpeg\r\n\r\n\xff\xd8\xff")
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		rst(hj)
	}))
	defer cut.Close()
	err = f.Relay(context.Background(), Camera{ID: "c", Asset: "A", Kind: KindMJPEG, URL: cut.URL}, nil, io.Discard, func() {}, nil)
	if err != nil && (strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "tcp")) {
		t.Errorf("a stream cut mid-frame: %v", err)
	}
}

// No credential goes out before the camera asks: a digest camera is never sent
// the password in basic, and a camera is answered in the scheme it asked for.
func TestCredentialsAreSentOnlyAfterTheChallengeAndInItsScheme(t *testing.T) {
	jb := jpegBytes()
	var firstAuth, basicSeen []string
	digestCam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if a == "" {
			firstAuth = append(firstAuth, "")
			w.Header().Set("Www-Authenticate", `Digest realm="cam", nonce="n1", qop="auth"`)
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(a, "Basic ") {
			basicSeen = append(basicSeen, a)
			w.WriteHeader(401)
			return
		}
		w.Write(jb)
	}))
	defer digestCam.Close()
	f := &Fetcher{Secret: secrets(map[string]string{"camera.d": "viewer:pw"})}
	if _, err := f.Snapshot(context.Background(), Camera{ID: "d", Asset: "A", Kind: KindSnapshot, URL: digestCam.URL, Credentials: true}); err != nil {
		t.Fatal(err)
	}
	if len(firstAuth) != 1 || len(basicSeen) != 0 {
		t.Errorf("a digest camera saw %d unauthenticated requests and %d basic headers: the password must not go out in basic", len(firstAuth), len(basicSeen))
	}
	// one that asks for basic is then sent it straight away
	var auths []string
	basicCam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		auths = append(auths, a)
		if a == "" {
			w.Header().Set("Www-Authenticate", `Basic realm="cam"`)
			w.WriteHeader(401)
			return
		}
		w.Write(jb)
	}))
	defer basicCam.Close()
	bc := Camera{ID: "b", Asset: "A", Kind: KindSnapshot, URL: basicCam.URL, Credentials: true}
	f.Secret = secrets(map[string]string{"camera.b": "viewer:pw"})
	for i := 0; i < 2; i++ {
		if _, err := f.Snapshot(context.Background(), bc); err != nil {
			t.Fatal(err)
		}
	}
	if len(auths) != 3 || auths[0] != "" || auths[1] == "" || auths[2] == "" {
		t.Errorf("basic camera saw %q: want one challenge, then credentials straight away", auths)
	}
	// a digest algorithm the controller does not speak is said, not guessed
	sha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Digest realm="cam", nonce="n", algorithm=SHA-256, qop="auth"`)
		w.WriteHeader(401)
	}))
	defer sha.Close()
	f.Secret = secrets(map[string]string{"camera.s": "viewer:pw"})
	if _, err := f.Snapshot(context.Background(), Camera{ID: "s", Asset: "A", Kind: KindSnapshot, URL: sha.URL, Credentials: true}); err == nil || !strings.Contains(err.Error(), "MD5 only") {
		t.Errorf("a SHA-256 digest camera: %v", err)
	}
}

// The credentials ride a redirect, so it stays on the same host, port and
// scheme: https to http would send them in the clear.
func TestARedirectKeepsHostPortAndScheme(t *testing.T) {
	check := (&Fetcher{}).client().CheckRedirect
	from := func(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }
	for _, tc := range []struct {
		to   string
		want bool // allowed
	}{
		{"https://cam.example:8443/b", true},
		{"https://cam.example:8443/", true},
		{"http://cam.example:8443/b", false},  // scheme downgrade
		{"https://cam.example:9443/b", false}, // another port
		{"https://other.example:8443/b", false},
	} {
		err := check(from(tc.to), []*http.Request{from("https://cam.example:8443/a")})
		if (err == nil) != tc.want {
			t.Errorf("redirect to %s: %v, allowed should be %v", tc.to, err, tc.want)
		}
	}
}

// A camera that sends a frame and goes quiet does not hold its place: the stream
// ends and says so.
func TestAStreamThatGoesQuietEnds(t *testing.T) {
	old := StreamIdle
	StreamIdle = 150 * time.Millisecond
	defer func() { StreamIdle = old }()
	jb := jpegBytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := multipart.NewWriter(w)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mw.Boundary())
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", "image/jpeg")
		pw, _ := mw.CreatePart(h)
		pw.Write(jb)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // then nothing, until the controller gives up
	}))
	defer srv.Close()
	done := make(chan error, 1)
	go func() {
		done <- (&Fetcher{}).Relay(context.Background(), Camera{ID: "q", Asset: "A", Kind: KindMJPEG, URL: srv.URL}, nil, io.Discard, func() {}, nil)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped sending") {
			t.Errorf("a quiet camera ended the stream with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a camera that went quiet held the stream for 5 s")
	}
}

func TestACredentialInAURLsQueryIsRefused(t *testing.T) {
	for _, u := range []string{"http://10.0.0.1/snap.jpg?user=a&pwd=b", "http://10.0.0.1/s?token=abc", "http://10.0.0.1/s?apikey=1", "http://10.0.0.1/s?Password=x"} {
		errs := Validate([]Camera{{ID: "a", Asset: "A", Kind: KindSnapshot, URL: u}})
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), "query") {
			t.Errorf("%s: %v", u, errs)
		}
		for _, e := range errs {
			if strings.Contains(e.Error(), "pwd=b") || strings.Contains(e.Error(), "abc") {
				t.Errorf("the error repeats the credential: %v", e)
			}
		}
	}
	if errs := Validate([]Camera{{ID: "a", Asset: "A", Kind: KindSnapshot, URL: "http://10.0.0.1/snap.jpg?channel=1&res=low"}}); len(errs) != 0 {
		t.Errorf("a harmless query refused: %v", errs)
	}
}

// rst closes a connection with a reset, so the reader's error names both ends
// ("read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer").
func rst(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.Close()
}
