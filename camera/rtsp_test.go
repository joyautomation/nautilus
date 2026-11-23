//go:build !js

package camera

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera/cameratest"
)

func rtspCam(f *cameratest.Server, creds bool) Camera {
	return Camera{ID: "gate", Asset: "A", Kind: KindRTSP, URL: f.URL("/stream"), Credentials: creds}
}

func hubFor(t *testing.T, secret string) *HLSHub {
	t.Helper()
	h := NewHLSHub(&Fetcher{Secret: secrets(map[string]string{"camera.gate": secret})}, nil)
	t.Cleanup(h.Stop)
	return h
}

// wait returns the session's stream ready, or the error it ended with.
func wait(t *testing.T, h *HLSHub, c Camera) (*HLSStream, error) {
	t.Helper()
	s, err := h.Session(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return s.Stream, s.Stream.Wait(ctx)
}

func noLeak(t *testing.T, what string, err error, extra ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, bad := range append([]string{"127.0.0.1", "localhost", "tcp", "hunter2"}, extra...) {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("%s: the error names %q: %v", what, bad, err)
		}
	}
}

func TestACameraBecomesAnHLSStreamOverRTSPInterleavedOnTCP(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Loops: 2})
	st, err := wait(t, hubFor(t, "viewer:hunter2"), rtspCam(f, false))
	if err != nil {
		t.Fatal(err)
	}
	pl := st.Playlist()
	if !strings.Contains(pl, "seg-1.m4s") || !strings.Contains(pl, "seg-2.m4s") {
		t.Errorf("playlist:\n%s", pl)
	}
	if !bytes.Contains(st.Init(), []byte("avcC")) {
		t.Error("no init segment")
	}
	seg, _ := st.Segment(1)
	fb := boxes(t, seg)
	var want []byte
	for _, au := range clipAUs(t)[:20] {
		want = append(want, AVCC(au.NALs)...)
	}
	if len(fb) != 2 || !bytes.Equal(fb[1].body, want) {
		t.Error("the first segment is not the clip's first twenty pictures")
	}
	if f.Plays.Load() != 1 {
		t.Errorf("%d PLAYs", f.Plays.Load())
	}
}

func TestParameterSetsComeFromTheStreamWhenTheDescriptionHasNone(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{SDP: "nosprop", Loops: 2})
	st, err := wait(t, hubFor(t, ""), rtspCam(f, false))
	if err != nil || !bytes.Contains(st.Init(), []byte("avcC")) {
		t.Fatalf("in-band parameter sets: %v", err)
	}
}

func TestDigestAndBasicCredentialsAreAnsweredOnlyAfterTheChallengeAndNeverDowngraded(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Auth: "digest", User: "viewer", Pwd: "hunter2", Loops: 2})
	if _, err := wait(t, hubFor(t, "viewer:hunter2"), rtspCam(f, true)); err != nil {
		t.Fatalf("digest: %v", err)
	}
	if f.Basic.Load() != 0 {
		t.Error("the password went out in basic to a digest camera")
	}
	// a wrong password is refused in words that carry neither the address nor the password
	g := cameratest.Start(t, cameratest.Opts{Auth: "digest", User: "viewer", Pwd: "hunter2"})
	_, err := wait(t, hubFor(t, "viewer:wrong"), rtspCam(g, true))
	if err == nil || !strings.Contains(err.Error(), "refused the controller's credentials") {
		t.Fatalf("a wrong password: %v", err)
	}
	noLeak(t, "wrong password", err, "wrong")
	// basic, when that is what the camera asks for
	b := cameratest.Start(t, cameratest.Opts{Auth: "basic", User: "viewer", Pwd: "hunter2", Loops: 2})
	if _, err := wait(t, hubFor(t, "viewer:hunter2"), rtspCam(b, true)); err != nil {
		t.Fatalf("basic: %v", err)
	}
	// no credential is sent before the camera asks, and none when the manifest has none
	n := cameratest.Start(t, cameratest.Opts{Auth: "digest", User: "viewer", Pwd: "hunter2"})
	_, err = wait(t, hubFor(t, ""), rtspCam(n, false))
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a camera that wants credentials the row does not have: %v", err)
	}
}

func TestWhatACameraOffersThatCannotBeRelayedIsSaidInPlainWords(t *testing.T) {
	for name, tc := range map[string]struct {
		o    cameratest.Opts
		want string
	}{
		"h265":       {cameratest.Opts{SDP: "h265"}, "another video codec"},
		"audio":      {cameratest.Opts{SDP: "audio"}, "no H.264"},
		"no sdp":     {cameratest.Opts{SDP: "nosdp"}, "no video track"},
		"404":        {cameratest.Opts{Misbehave: "play-404"}, "no such stream"},
		"tcp only":   {cameratest.Opts{Misbehave: "setup-udp"}, "TCP"},
		"no session": {cameratest.Opts{Misbehave: "no-session"}, "no session"},
	} {
		f := cameratest.Start(t, tc.o)
		_, err := wait(t, hubFor(t, ""), rtspCam(f, false))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, tc.want)
		}
		noLeak(t, name, err)
	}
}

func TestAHostileCameraEndsTheSessionWithoutGrowingMemoryOrNamingItself(t *testing.T) {
	for _, mis := range []string{"huge-body", "endless-headers", "garbage", "long-line"} {
		f := cameratest.Start(t, cameratest.Opts{Misbehave: mis})
		_, err := wait(t, hubFor(t, ""), rtspCam(f, false))
		if err == nil {
			t.Errorf("%s: the session did not end with an error", mis)
		}
		noLeak(t, mis, err)
	}
}

func TestACameraThatGoesQuietOrNeverAnswersPlayEndsTheSession(t *testing.T) {
	old := StreamIdle
	StreamIdle = 300 * time.Millisecond
	defer func() { StreamIdle = old }()
	f := cameratest.Start(t, cameratest.Opts{Misbehave: "quiet"})
	_, err := wait(t, hubFor(t, ""), rtspCam(f, false))
	if err == nil || !strings.Contains(err.Error(), "stopped sending") {
		t.Errorf("a camera that sends nothing after PLAY: %v", err)
	}
	noLeak(t, "quiet", err)
}

func TestBadRTPPacketsAreLossesNotTheEndOfTheSession(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Misbehave: "bad-rtp", Loops: 6})
	st, err := wait(t, hubFor(t, ""), rtspCam(f, false))
	if err != nil {
		t.Fatalf("a camera that sends some garbage frames: %v", err)
	}
	if strings.Count(st.Playlist(), "#EXTINF") == 0 {
		t.Error("no segment survived the losses")
	}
}

func TestASessionIsStoppedWhenNobodyAsksForIt(t *testing.T) {
	old := HLSIdle
	HLSIdle = 400 * time.Millisecond
	defer func() { HLSIdle = old }()
	f := cameratest.Start(t, cameratest.Opts{Loops: 2})
	h := hubFor(t, "")
	if _, err := wait(t, h, rtspCam(f, false)); err != nil {
		t.Fatal(err)
	}
	if h.Active() != 1 {
		t.Fatalf("%d sessions", h.Active())
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.Active() != 0 {
		t.Error("an unwatched session was never stopped")
	}
	// the next request starts a fresh one
	if _, err := wait(t, h, rtspCam(f, false)); err != nil || f.Plays.Load() != 2 {
		t.Errorf("a new session: %v, %d PLAYs", err, f.Plays.Load())
	}
}

func TestAControlURLNamingAnotherHostIsNotFollowed(t *testing.T) {
	c := &rtspClient{base: "rtsp://10.0.0.5/stream"}
	for control, want := range map[string]string{
		"track1":                        "rtsp://10.0.0.5/stream/track1",
		"":                              "rtsp://10.0.0.5/stream",
		"*":                             "rtsp://10.0.0.5/stream",
		"rtsp://10.0.0.5/stream/track9": "rtsp://10.0.0.5/stream/track9",
		"rtsp://evil.example/steal":     "rtsp://10.0.0.5/stream",
		"RTSP://10.0.0.6/other":         "rtsp://10.0.0.5/stream",
	} {
		if got := c.trackURL(control); got != want {
			t.Errorf("control %q -> %q, want %q", control, got, want)
		}
	}
}

func TestDialErrorsNameNeitherTheAddressNorTheURL(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	_, err := dialRTSP(context.Background(), "rtsp://viewer:hunter2@"+addr+"/x", "viewer", "hunter2")
	if err == nil {
		t.Fatal("dialled a closed port")
	}
	noLeak(t, "dial", err, addr)
	if _, err := dialRTSP(context.Background(), "http://x/y", "", ""); err == nil || strings.Contains(err.Error(), "x/y") {
		t.Errorf("a URL that is not RTSP: %v", err)
	}
}

// A camera's 401 with a WWW-Authenticate that is not UTF-8 once crashed the
// controller (an index into the lower-cased header sliced the original): it is an
// error now, and the pipeline survives anything.
func TestAChallengeThatIsNotUTF8DoesNotCrashTheController(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Misbehave: "bad-challenge"})
	_, err := wait(t, hubFor(t, "viewer:hunter2"), rtspCam(f, true))
	if err == nil {
		t.Fatal("a camera with a hostile challenge gave a stream")
	}
	noLeak(t, "bad challenge", err)
	d, basic := pickChallenge([]string{"\xff\xff\xff\xff\xff\xff\xff\xff\xff digest realm=x", "\xffBasic"})
	if d == "" || basic {
		t.Errorf("pickChallenge: %q %v", d, basic)
	}
}

func TestADigestCameraIsNotDowngradedToBasicMidSession(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Auth: "digest", User: "viewer", Pwd: "hunter2", Misbehave: "downgrade"})
	_, err := wait(t, hubFor(t, "viewer:hunter2"), rtspCam(f, true))
	if err == nil || !strings.Contains(err.Error(), "not sending the password") {
		t.Errorf("a camera that asked for digest and then for basic: %v", err)
	}
	if f.Basic.Load() != 0 {
		t.Errorf("the password went out in basic %d times", f.Basic.Load())
	}
}

func TestAContentBaseOnAnotherHostIsIgnoredAndAControlWithSpacesIsNot(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{Misbehave: "content-base", Loops: 2})
	if _, err := wait(t, hubFor(t, ""), rtspCam(f, false)); err != nil {
		t.Fatalf("a camera naming another host as its base: %v", err)
	}
	c := &rtspClient{base: "rtsp://10.0.0.5/stream"}
	if got := c.trackURL("track 1\r\nX: y"); got != "rtsp://10.0.0.5/stream" {
		t.Errorf("a control with whitespace: %q", got)
	}
}

// A camera that fails is not opened again at once, and a person who asks for every
// camera cannot hold more sessions than the cap.
func TestAFailingCameraIsNotRedialledAtOnceAndSessionsAreCapped(t *testing.T) {
	f := cameratest.Start(t, cameratest.Opts{SDP: "h265"})
	h := hubFor(t, "")
	c := rtspCam(f, false)
	if _, err := wait(t, h, c); err == nil {
		t.Fatal("an H.265 camera played")
	}
	plays := f.Requests.Load()
	for i := 0; i < 5; i++ {
		if _, err := h.Session(c); err == nil {
			t.Fatal("a failed camera was reopened inside the backoff")
		}
	}
	if f.Requests.Load() != plays {
		t.Errorf("a failed camera was redialled: %d requests, was %d", f.Requests.Load(), plays)
	}
	good := cameratest.Start(t, cameratest.Opts{Loops: 2})
	h2 := hubFor(t, "")
	opened := 0
	for i := 0; i < MaxLive+3; i++ {
		cam := Camera{ID: "c" + string(rune('a'+i)), Asset: "A", Kind: KindRTSP, URL: good.URL("/s")}
		if _, err := h2.Session(cam); err == nil {
			opened++
		} else if err != ErrTooManyStreams {
			t.Fatal(err)
		}
	}
	if opened != MaxLive {
		t.Errorf("%d sessions opened, want the cap %d", opened, MaxLive)
	}
}

// What a camera's challenge says is echoed back in the Authorization header: a line
// break in it must not become a header or a request of its own.
func TestAChallengeWithALineBreakInItIsRefused(t *testing.T) {
	for _, ch := range []string{"Digest realm=\"a\rb\", nonce=\"n\"", "Digest realm=\"a\", nonce=\"n\r\nX: y\"", "Digest realm=\"a\", nonce=\"n\", opaque=\"o\x00\""} {
		if _, err := digest(ch, "viewer", "pw", "GET", "/x"); err == nil {
			t.Errorf("%q was answered", ch)
		}
	}
}
