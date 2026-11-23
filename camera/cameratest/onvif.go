package cameratest

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ONVIFProfile is one media profile of a fake device.
type ONVIFProfile struct {
	Token, Name, Encoding string
	W, H                  int
}

// ONVIFOpts shapes a fake ONVIF device.
type ONVIFOpts struct {
	User, Pass string // when set, requests must carry a good WS-Security digest (or, with HTTPAuth, basic)
	HTTPAuth   bool   // ask for HTTP basic instead of WS-Security
	Profiles   []ONVIFProfile
	Stream     string // the stream URI the device hands out (default rtsp://10.9.9.9:554/live: a host the controller must replace)
	Snapshot   string // "" none; the device hands it out as given
	Skew       time.Duration
	// Misbehave: "fault", "huge", "garbage", "no-media", "bad-scheme", "userinfo", "evil-token", "slow", "doctype"
	Misbehave string
}

// ONVIF is a fake ONVIF device: the device service and the media service on one
// port, recording what it was asked.
type ONVIF struct {
	srv *httptest.Server
	o   ONVIFOpts
	mu  sync.Mutex
	// Calls is each operation asked, in order ("GetProfiles", ...).
	calls []string
	// Rejected counts requests refused for bad credentials.
	rejected int
	// Tokens is the Username and digest of each signed request, for tests that check them.
	signed int
}

// StartONVIF starts a fake device; it stops with the test.
func StartONVIF(t testing.TB, o ONVIFOpts) *ONVIF {
	if len(o.Profiles) == 0 {
		o.Profiles = []ONVIFProfile{
			{"main", "Main", "H264", 1920, 1080},
			{"sub", "Sub", "H264", 640, 360},
			{"hevc", "Hevc", "H265", 320, 240},
		}
	}
	if o.Stream == "" {
		o.Stream = "rtsp://10.9.9.9:554/live"
	}
	d := &ONVIF{o: o}
	d.srv = httptest.NewServer(http.HandlerFunc(d.handle))
	t.Cleanup(d.srv.Close)
	return d
}

// URL is the device service address a manifest row names.
func (d *ONVIF) URL() string { return d.srv.URL + "/onvif/device_service" }

// Calls returns the operations asked so far.
func (d *ONVIF) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// Rejected is how many requests were refused for credentials.
func (d *ONVIF) Rejected() int { d.mu.Lock(); defer d.mu.Unlock(); return d.rejected }

func (d *ONVIF) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	body := string(b)
	op := operation(body)
	d.mu.Lock()
	d.calls = append(d.calls, op)
	d.mu.Unlock()
	if d.o.Misbehave == "slow" {
		time.Sleep(15 * time.Second)
	}
	// GetSystemDateAndTime is answered without credentials, as on a real device
	if d.o.User != "" && op != "GetSystemDateAndTime" && !d.authorized(r, body) {
		d.mu.Lock()
		d.rejected++
		d.mu.Unlock()
		if d.o.HTTPAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="onvif"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch d.o.Misbehave {
	case "garbage":
		w.Write([]byte("<html>not soap"))
		return
	case "huge":
		w.Write([]byte("<s:Envelope xmlns:s=\"x\"><s:Body>" + strings.Repeat("a", 2<<20)))
		return
	case "fault":
		w.WriteHeader(500)
		w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>s:Sender</s:Value></s:Code><s:Reason><s:Text xml:lang="en">Sender not authorized` + "\n\t" + `</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`))
		return
	case "doctype":
		w.Write([]byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]><s:Envelope xmlns:s="x"><s:Body>&b;</s:Body></s:Envelope>`))
		return
	}
	w.Header().Set("Content-Type", "application/soap+xml")
	w.Write([]byte(d.answer(op, body)))
}

func operation(body string) string {
	for _, op := range []string{"GetSystemDateAndTime", "GetCapabilities", "GetProfiles", "GetStreamUri", "GetSnapshotUri", "ContinuousMove", "Stop", "GotoPreset", "GetPresets", "GetConfigurations"} {
		if strings.Contains(body, ":"+op) || strings.Contains(body, "<"+op) {
			return op
		}
	}
	return "unknown"
}

func (d *ONVIF) authorized(r *http.Request, body string) bool {
	if d.o.HTTPAuth {
		u, p, ok := r.BasicAuth()
		return ok && u == d.o.User && p == d.o.Pass
	}
	var env struct {
		User    string `xml:"Header>Security>UsernameToken>Username"`
		Pass    string `xml:"Header>Security>UsernameToken>Password"`
		Nonce   string `xml:"Header>Security>UsernameToken>Nonce"`
		Created string `xml:"Header>Security>UsernameToken>Created"`
	}
	if xml.Unmarshal([]byte(body), &env) != nil || env.User != d.o.User {
		return false
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return false
	}
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(env.Created))
	h.Write([]byte(d.o.Pass))
	if base64.StdEncoding.EncodeToString(h.Sum(nil)) != env.Pass {
		return false
	}
	// the token's time must be near the device's own (skewed) clock
	at, err := time.Parse("2006-01-02T15:04:05Z", env.Created)
	if err != nil {
		return false
	}
	diff := time.Until(at) - d.o.Skew
	return diff < 30*time.Second && diff > -30*time.Second
}

func (d *ONVIF) answer(op, body string) string {
	const head = `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>`
	const tail = `</s:Body></s:Envelope>`
	switch op {
	case "GetSystemDateAndTime":
		t := time.Now().UTC().Add(d.o.Skew)
		return head + fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse xmlns:tds="x" xmlns:tt="y"><tds:SystemDateAndTime><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
			t.Hour(), t.Minute(), t.Second(), t.Year(), int(t.Month()), t.Day()) + tail
	case "GetCapabilities":
		media := d.srv.URL + "/onvif/media_service"
		if d.o.Misbehave == "no-media" {
			return head + `<tds:GetCapabilitiesResponse xmlns:tds="x"><tds:Capabilities></tds:Capabilities></tds:GetCapabilitiesResponse>` + tail
		}
		if d.o.Misbehave == "bad-scheme" {
			media = "file:///etc/passwd"
		}
		// the device names itself by an address of its own: the controller must use the host of its manifest
		media = strings.Replace(media, "127.0.0.1", "10.9.9.9", 1)
		return head + `<tds:GetCapabilitiesResponse xmlns:tds="x" xmlns:tt="y"><tds:Capabilities><tt:Media><tt:XAddr>` + media + `</tt:XAddr></tt:Media><tt:PTZ><tt:XAddr>` + strings.Replace(d.srv.URL, "127.0.0.1", "10.9.9.9", 1) + `/onvif/ptz_service</tt:XAddr></tt:PTZ></tds:Capabilities></tds:GetCapabilitiesResponse>` + tail
	case "GetProfiles":
		var b strings.Builder
		for _, p := range d.o.Profiles {
			tok := p.Token
			if d.o.Misbehave == "evil-token" {
				tok = `x"><evil/>`
			}
			fmt.Fprintf(&b, `<trt:Profiles token="%s" fixed="true"><tt:Name>%s</tt:Name><tt:VideoEncoderConfiguration token="v%s"><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>`,
				strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(tok), p.Name, p.Token, p.Encoding, p.W, p.H)
		}
		return head + `<trt:GetProfilesResponse xmlns:trt="x" xmlns:tt="y">` + b.String() + `</trt:GetProfilesResponse>` + tail
	case "GetStreamUri":
		uri := d.o.Stream
		switch d.o.Misbehave {
		case "bad-scheme":
			uri = "http://10.9.9.9/x"
		case "userinfo":
			uri = "rtsp://admin:secret@10.9.9.9:554/live"
		}
		// which profile was asked for is in the request: a camera answers a different address per profile
		tok := between(body, "ProfileToken>", "<")
		return head + `<trt:GetStreamUriResponse xmlns:trt="x" xmlns:tt="y"><trt:MediaUri><tt:Uri>` + strings.Replace(uri, "/live", "/live/"+tok, 1) + `</tt:Uri></trt:MediaUri></trt:GetStreamUriResponse>` + tail
	case "GetSnapshotUri":
		if d.o.Snapshot == "" {
			return head + `<s:Fault><s:Code><s:Value>s:Receiver</s:Value></s:Code><s:Reason><s:Text>no snapshot</s:Text></s:Reason></s:Fault>` + tail
		}
		return head + `<trt:GetSnapshotUriResponse xmlns:trt="x" xmlns:tt="y"><trt:MediaUri><tt:Uri>` + d.o.Snapshot + `</tt:Uri></trt:MediaUri></trt:GetSnapshotUriResponse>` + tail
	}
	return head + tail
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}
