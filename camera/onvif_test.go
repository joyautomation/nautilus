package camera

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera/cameratest"
)

func onvifRow(d *cameratest.ONVIF) Camera {
	return Camera{ID: "dome", Label: "Dome", Asset: "pump1", Kind: KindONVIF, URL: d.URL(), Credentials: true}
}

func TestAnONVIFDeviceIsResolvedToItsSubStreamOnTheManifestsHost(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{User: "admin", Pass: "pw", Snapshot: "http://10.9.9.9:8080/snap.jpg?profile=sub"})
	f := &Fetcher{Secret: secrets(map[string]string{"camera.dome": "admin:pw"})}
	c, err := f.Resolve(context.Background(), onvifRow(d))
	if err != nil {
		t.Fatal(err)
	}
	// the smallest H.264 profile (not the larger one, not the H.265 one)
	if c.Kind != KindRTSP || !strings.HasSuffix(c.URL, "/live/sub") {
		t.Errorf("resolved to %s %q", c.Kind, c.URL)
	}
	// the device named itself 10.9.9.9: the manifest's host is the one used, and the port the device named is kept
	host := strings.TrimPrefix(d.URL(), "http://")
	host = host[:strings.Index(host, ":")]
	if !strings.HasPrefix(c.URL, "rtsp://"+host+":554/") {
		t.Errorf("the stream address %q is not on the manifest's host %s", c.URL, host)
	}
	if !strings.HasPrefix(c.SnapshotURL, "http://"+host+":8080/snap.jpg") || strings.Contains(c.SnapshotURL+c.URL, "10.9.9.9") {
		t.Errorf("the snapshot address %q", c.SnapshotURL)
	}
	// asked once, then kept
	n := len(d.Calls())
	if _, err := f.Resolve(context.Background(), onvifRow(d)); err != nil || len(d.Calls()) != n {
		t.Errorf("a resolved device was asked again: %v", d.Calls())
	}
	// a row that names a profile gets that one, a row that names a missing one is told so
	r := onvifRow(d)
	r.ID, r.Profile = "dome2", "Main"
	f.Secret = secrets(map[string]string{"camera.dome": "admin:pw", "camera.dome2": "admin:pw", "camera.dome3": "admin:pw"})
	if c, err = f.Resolve(context.Background(), r); err != nil || !strings.HasSuffix(c.URL, "/live/main") {
		t.Errorf("profile Main: %q %v", c.URL, err)
	}
	r.ID, r.Profile = "dome3", "nope"
	if _, err = f.Resolve(context.Background(), r); err == nil || !strings.Contains(err.Error(), "no profile") {
		t.Errorf("a profile the device lacks: %v", err)
	}
}

func TestONVIFCredentialsGoOutAsADigestAndAWrongPasswordIsRefusedPlainly(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{User: "admin", Pass: "s3cret-pw"})
	f := &Fetcher{Secret: secrets(map[string]string{"camera.dome": "admin:wrong"})}
	_, err := f.Resolve(context.Background(), onvifRow(d))
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("wrong password: %v", err)
	}
	if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the error names a password: %v", err)
	}
	if d.Rejected() == 0 {
		t.Error("the device never saw the bad token")
	}
	// a camera whose clock is an hour off still accepts the token: the controller reads the device's time first
	d2 := cameratest.StartONVIF(t, cameratest.ONVIFOpts{User: "admin", Pass: "pw", Skew: time.Hour})
	f2 := &Fetcher{Secret: secrets(map[string]string{"camera.dome": "admin:pw"})}
	if _, err := f2.Resolve(context.Background(), onvifRow(d2)); err != nil {
		t.Errorf("a skewed device: %v", err)
	}
	// and one that asks for HTTP basic is sent it only after it asks
	d3 := cameratest.StartONVIF(t, cameratest.ONVIFOpts{User: "admin", Pass: "pw", HTTPAuth: true})
	if _, err := f2.Resolve(context.Background(), onvifRow(d3)); err != nil {
		t.Errorf("an HTTP-auth device: %v", err)
	}
}

func TestWhatAnONVIFDeviceSendsIsNeverTrustedOrFollowed(t *testing.T) {
	for _, tc := range []struct{ mis, want string }{
		{"garbage", "not SOAP"},
		{"huge", "larger than"},
		{"fault", "refused: Sendernotauthorized"},
		{"no-media", "no media service"},
		{"bad-media-scheme", "no media service"},
		{"bad-stream-scheme", "the stream address: not a rtsp address"},
		{"userinfo", ""}, // the credentials in the stream address are dropped, not used
		{"evil-token", "no H.264 profile"},
		{"doctype", "not SOAP"},
	} {
		d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Misbehave: tc.mis})
		c := onvifRow(d)
		c.Credentials = false
		f := &Fetcher{}
		got, err := f.Resolve(context.Background(), c)
		switch tc.mis {
		case "userinfo":
			if err != nil || strings.Contains(got.URL, "secret") || strings.Contains(got.URL, "admin") {
				t.Errorf("%s: %q %v", tc.mis, got.URL, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.mis, err, tc.want)
			continue
		}
		if strings.ContainsAny(err.Error(), "\n\t") || len(err.Error()) > 300 {
			t.Errorf("%s: the error carries raw device text: %q", tc.mis, err)
		}
	}
}

func TestADeviceThatIsDownIsNotAskedAtEveryRequest(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Misbehave: "garbage"})
	c := onvifRow(d)
	c.Credentials = false
	f := &Fetcher{}
	f.Resolve(context.Background(), c)
	n := len(d.Calls())
	if _, err := f.Resolve(context.Background(), c); err == nil || len(d.Calls()) != n {
		t.Errorf("a failure was asked again within %s: %d calls, was %d", OnvifRetry, len(d.Calls()), n)
	}
}

func TestAnONVIFCameraIsAnHLSCameraWithAStillAndPinsNoHostOfItsOwn(t *testing.T) {
	c := Camera{ID: "d", Asset: "a", Kind: KindONVIF, URL: "http://10.0.0.9/onvif/device_service"}
	if !c.HLS() || !c.Live() || !c.HasSnapshot() {
		t.Error("an ONVIF camera streams HLS and has a still")
	}
	errs := Validate([]Camera{c, {ID: "e", Asset: "a", Kind: KindRTSP, URL: "rtsp://x/y", Profile: "main"}, {ID: "f", Asset: "a", Kind: KindONVIF, URL: "http://x/y", Profile: `a"b`}})
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), "profile") || !strings.Contains(errs[1].Error(), "profile") {
		t.Errorf("a profile on a non-ONVIF row or with a bad name: %v", errs)
	}
}

func TestAnONVIFCameraStreamsThroughTheSameRelayAndAStillComesFromItsSnapshotAddress(t *testing.T) {
	rt := cameratest.Start(t, cameratest.Opts{Loops: 2})
	port := rt.Addr()[strings.LastIndex(rt.Addr(), ":")+1:]
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{User: "admin", Pass: "pw", Stream: "rtsp://10.9.9.9:" + port + "/live"})
	h := NewHLSHub(&Fetcher{Secret: secrets(map[string]string{"camera.dome": "admin:pw"})}, nil)
	t.Cleanup(h.Stop)
	st, err := wait(t, h, onvifRow(d))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Playlist(), "seg-1.m4s") {
		t.Errorf("no playlist:\n%s", st.Playlist())
	}
	// a device with no snapshot answers with a fault: a still is not available, said plainly
	f := &Fetcher{Secret: secrets(map[string]string{"camera.dome": "admin:pw"})}
	if _, err := f.Snapshot(context.Background(), onvifRow(d)); err == nil || err.Error() != ErrNoSnapshot.Error() {
		t.Errorf("a device without a snapshot address: %v", err)
	}
}

func TestADeviceReachedOverHTTPSIsNeverSentTheCredentialsOverHTTPAndKeepsItsPort(t *testing.T) {
	pin := func(dev, raw string, schemes ...string) string {
		u, _ := url.Parse(dev)
		out, err := (&soap{dev: u}).pin(raw, schemes...)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out
	}
	for _, tc := range []struct{ dev, raw, want string }{
		// a device reached over https names its services as http: they stay https
		{"https://cam:8443/onvif/device_service", "http://192.168.1.64/onvif/media", "https://cam:8443/onvif/media"},
		// a device behind a port forward that names no port is reached on the manifest's
		{"http://gw:8081/onvif/device_service", "http://192.168.1.64/onvif/media", "http://gw:8081/onvif/media"},
		// one that names a port keeps it
		{"http://gw:8081/onvif/device_service", "http://192.168.1.64:8080/onvif/media", "http://gw:8080/onvif/media"},
		// the stream is RTSP: the manifest's HTTP port is not its port
		{"http://gw:8081/onvif/device_service", "rtsp://192.168.1.64/live", "rtsp://gw/live"},
		// a plain device on a plain port
		{"http://cam/onvif/device_service", "HTTP://192.168.1.64/x?a=1", "http://cam/x?a=1"},
		{"http://cam/d", "http://cam:0/x", "ERR a port that is not one"},
		{"http://cam/d", "http://cam:99999/x", "ERR a port that is not one"},
		{"http://[fe80::1]/d", "http://10.0.0.1:8000/x", "http://[fe80::1]:8000/x"},
	} {
		sc := []string{"http", "https"}
		if strings.HasPrefix(tc.raw, "rtsp") {
			sc = []string{"rtsp"}
		}
		if got := pin(tc.dev, tc.raw, sc...); got != tc.want {
			t.Errorf("%s names %s: got %s, want %s", tc.dev, tc.raw, got, tc.want)
		}
	}
	// what one service of a device asks for is not what another is sent
	if learnKey("c", "http://cam:80/soap") == learnKey("c", "http://cam:8080/snap") {
		t.Error("two services of one device share what they asked for")
	}
}

func TestASnapshotAddressThatCouldNotBeAskedForIsNotRememberedAsNone(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Misbehave: "snapshot-503"})
	c := onvifRow(d)
	c.Credentials = false
	f := &Fetcher{}
	if _, err := f.Resolve(context.Background(), c); err == nil || !strings.Contains(err.Error(), "snapshot address") {
		t.Errorf("a snapshot request that failed in transit was taken as 'none': %v", err)
	}
}

func TestAPlayingStreamIsNotHeldUpWhenTheDeviceIsGoneAndItsAnswerExpired(t *testing.T) {
	old := OnvifTTL
	OnvifTTL = time.Millisecond
	t.Cleanup(func() { OnvifTTL = old })
	rt := cameratest.Start(t, cameratest.Opts{Loops: -1, Pause: 50 * time.Millisecond})
	port := rt.Addr()[strings.LastIndex(rt.Addr(), ":")+1:]
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Stream: "rtsp://10.9.9.9:" + port + "/live"})
	c := onvifRow(d)
	c.Credentials = false
	h := NewHLSHub(&Fetcher{}, nil)
	t.Cleanup(h.Stop)
	s1, err := h.Session(c)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	d.Close()
	s2, err := h.Session(c)
	if err != nil || s2 != s1 {
		t.Errorf("the playing session was not returned while the device is down: %v", err)
	}
}
