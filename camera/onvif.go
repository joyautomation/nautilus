//go:build !js

package camera

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ONVIF (#724 slice 4): a camera row of kind onvif names the device's service
// URL (http://host/onvif/device_service); the controller asks the device where
// its media service is, which profile to use, and the profile's RTSP stream and
// snapshot URIs, then treats the camera as an RTSP one.
//
// Everything a device answers is data. The address a device hands back is never
// followed as given: the scheme must be the one asked for and the host is
// always the manifest's own (a device behind NAT or a bad firmware names an
// address of its own choosing, and the controller reaches only the hosts the
// manifest lists). The credentials ride in a WS-Security UsernameToken digest
// (the password never crosses the wire in it), and, if the device asks for HTTP
// authentication instead, in the scheme it asks for, as for any camera.

// OnvifTTL is a variable so a test need not wait ten minutes.
var OnvifTTL = 10 * time.Minute

const (
	// MaxSOAPBytes bounds one device answer: profile lists are a few kilobytes.
	MaxSOAPBytes = 1 << 20
	// OnvifRetry: a resolved address is kept for OnvifTTL; a failure is kept for
	// OnvifRetry, so a camera that is down is not asked at every request.
	OnvifRetry = 5 * time.Second
	onvifTime  = 10 * time.Second
)

const (
	nsSOAP  = "http://www.w3.org/2003/05/soap-envelope"
	nsDev   = "http://www.onvif.org/ver10/device/wsdl"
	nsMedia = "http://www.onvif.org/ver10/media/wsdl"
	nsPTZ   = "http://www.onvif.org/ver20/ptz/wsdl"
	nsSchem = "http://www.onvif.org/ver10/schema"
	nsWSSE  = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	nsWSU   = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
	pwDig   = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"
)

// Resolved is what an ONVIF device said about itself.
type Resolved struct {
	Stream   string // rtsp:// URL, host the manifest's
	Snapshot string // http(s):// URL or "" when the device has none
	Profile  string // the profile token used
	Media    string // the device's media service URL
	PTZ      string // its PTZ service URL or ""
	Width    int
	Height   int
}

type onvifEntry struct {
	mu   sync.Mutex
	res  Resolved
	err  error
	at   time.Time
	have bool
}

// Resolve returns c with an ONVIF device's stream and snapshot filled in: an RTSP
// camera (kind rtsp, the stream URL, the snapshot URL unless the row names one).
// Any other kind is returned as it is.
func (f *Fetcher) Resolve(ctx context.Context, c Camera) (Camera, error) {
	if c.Kind != KindONVIF {
		return c, nil
	}
	r, err := f.resolveONVIF(ctx, c)
	if err != nil {
		return c, err
	}
	c.Kind = KindRTSP
	c.URL = r.Stream
	if c.SnapshotURL == "" {
		c.SnapshotURL = r.Snapshot
	}
	return c, nil
}

// ONVIF returns what the device said, resolving it if it is not kept.
func (f *Fetcher) ONVIF(ctx context.Context, c Camera) (Resolved, error) {
	if c.Kind != KindONVIF {
		return Resolved{}, errors.New("camera: not an ONVIF camera")
	}
	return f.resolveONVIF(ctx, c)
}

func (f *Fetcher) resolveONVIF(ctx context.Context, c Camera) (Resolved, error) {
	v, _ := f.onvif.LoadOrStore(c.ID, &onvifEntry{})
	e := v.(*onvifEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.have {
		ttl := OnvifTTL
		if e.err != nil {
			ttl = OnvifRetry
		}
		if time.Since(e.at) < ttl {
			return e.res, e.err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*onvifTime)
	defer cancel()
	res, err := f.discoverONVIF(ctx, c)
	if errors.Is(err, context.Canceled) {
		return Resolved{}, err // the caller went away: nothing is known
	}
	e.res, e.err, e.at, e.have = res, err, time.Now(), true
	return res, err
}

// discoverONVIF asks the device. Every step is one SOAP call with its own bound.
func (f *Fetcher) discoverONVIF(ctx context.Context, c Camera) (Resolved, error) {
	var out Resolved
	dev, err := url.Parse(c.URL)
	if err != nil || dev.Host == "" {
		return out, errors.New("the ONVIF camera's url is not a URL with a host")
	}
	cl := &soap{f: f, c: c, dev: dev}
	cl.skew(ctx) // before the first signed call: a camera with a wrong clock refuses a stale token

	var caps struct {
		Media struct {
			XAddr string `xml:"XAddr"`
		} `xml:"Body>GetCapabilitiesResponse>Capabilities>Media"`
		PTZ struct {
			XAddr string `xml:"XAddr"`
		} `xml:"Body>GetCapabilitiesResponse>Capabilities>PTZ"`
	}
	if err := cl.call(ctx, c.URL, `<tds:GetCapabilities xmlns:tds="`+nsDev+`"><tds:Category>All</tds:Category></tds:GetCapabilities>`, &caps); err != nil {
		return out, fmt.Errorf("ONVIF: asking the device for its services: %w", err)
	}
	media, err := cl.pin(caps.Media.XAddr, "http", "https")
	if err != nil || caps.Media.XAddr == "" {
		return out, errors.New("ONVIF: the device named no media service")
	}
	out.Media = media
	if caps.PTZ.XAddr != "" {
		if p, err := cl.pin(caps.PTZ.XAddr, "http", "https"); err == nil {
			out.PTZ = p
		}
	}

	var profs struct {
		Profiles []struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
			Video struct {
				Encoding   string `xml:"Encoding"`
				Resolution struct {
					Width  int `xml:"Width"`
					Height int `xml:"Height"`
				} `xml:"Resolution"`
			} `xml:"VideoEncoderConfiguration"`
		} `xml:"Body>GetProfilesResponse>Profiles"`
	}
	if err := cl.call(ctx, media, `<trt:GetProfiles xmlns:trt="`+nsMedia+`"/>`, &profs); err != nil {
		return out, fmt.Errorf("ONVIF: asking the device for its profiles: %w", err)
	}
	// the profile: the one the row names, else the smallest H.264 picture (the
	// camera's sub stream: the controller relays, it does not decode, and a small
	// stream is what a plant's link and a small core can carry)
	best := -1
	for i, p := range profs.Profiles {
		if !validToken(p.Token) {
			continue
		}
		if c.Profile != "" {
			if p.Token == c.Profile || p.Name == c.Profile {
				best = i
				break
			}
			continue
		}
		if !strings.EqualFold(strings.ReplaceAll(p.Video.Encoding, ".", ""), "H264") {
			continue
		}
		a := area(p.Video.Resolution.Width, p.Video.Resolution.Height)
		if best < 0 || a < profileArea(profs.Profiles[best].Video.Resolution.Width, profs.Profiles[best].Video.Resolution.Height) {
			best = i
		}
	}
	if best < 0 {
		if c.Profile != "" {
			return out, errors.New("ONVIF: the device has no profile of the name the row asks for")
		}
		return out, errors.New("ONVIF: the device has no H.264 profile (set its stream to H.264: H.265 is not relayed)")
	}
	p := profs.Profiles[best]
	out.Profile, out.Width, out.Height = p.Token, p.Video.Resolution.Width, p.Video.Resolution.Height

	var su struct {
		URI string `xml:"Body>GetStreamUriResponse>MediaUri>Uri"`
	}
	req := `<trt:GetStreamUri xmlns:trt="` + nsMedia + `" xmlns:tt="` + nsSchem + `"><trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup><trt:ProfileToken>` + p.Token + `</trt:ProfileToken></trt:GetStreamUri>`
	if err := cl.call(ctx, media, req, &su); err != nil {
		return out, fmt.Errorf("ONVIF: asking the device for the stream address: %w", err)
	}
	if out.Stream, err = cl.pin(su.URI, "rtsp"); err != nil {
		return out, fmt.Errorf("ONVIF: the stream address: %w", err)
	}

	// a snapshot is optional: a device without one still streams
	var sn struct {
		URI string `xml:"Body>GetSnapshotUriResponse>MediaUri>Uri"`
	}
	switch err := cl.call(ctx, media, `<trt:GetSnapshotUri xmlns:trt="`+nsMedia+`"><trt:ProfileToken>`+p.Token+`</trt:ProfileToken></trt:GetSnapshotUri>`, &sn); {
	case err == nil:
		if s, err := cl.pin(sn.URI, "http", "https"); err == nil {
			out.Snapshot = s
		}
	case errors.As(err, new(*soapFault)):
		// the device says it has none
	default:
		return out, fmt.Errorf("ONVIF: asking the device for the snapshot address: %w", err) // not cached as "none"
	}
	return out, nil
}

// area is a picture's size; a profile that states none is the largest, not the smallest.
func area(w, h int) int {
	if w <= 0 || h <= 0 {
		return math.MaxInt32
	}
	return w * h
}

func profileArea(w, h int) int { return area(w, h) }

// soap is one device's SOAP client.
type soap struct {
	f      *Fetcher
	c      Camera
	dev    *url.URL
	offset time.Duration // the device's clock minus ours
}

// skew reads the device's clock (a call that needs no credentials), so the
// token's timestamp is one the device accepts. A device that does not answer
// leaves the offset zero.
func (s *soap) skew(ctx context.Context) {
	var r struct {
		Year   int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Date>Year"`
		Month  int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Date>Month"`
		Day    int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Date>Day"`
		Hour   int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Time>Hour"`
		Minute int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Time>Minute"`
		Second int `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime>Time>Second"`
	}
	cctx, cancel := context.WithTimeout(ctx, onvifTime)
	defer cancel()
	if err := s.send(cctx, s.dev.String(), `<tds:GetSystemDateAndTime xmlns:tds="`+nsDev+`"/>`, &r, false); err != nil || r.Year < 2000 {
		return
	}
	dt := time.Date(r.Year, time.Month(r.Month), r.Day, r.Hour, r.Minute, r.Second, 0, time.UTC)
	if d := time.Until(dt); d > -30*365*24*time.Hour && d < 30*365*24*time.Hour {
		s.offset = d
	}
}

// pin makes an address a device handed back one the controller may use: one of
// the schemes, no user information, and the host the manifest's own (the port
// the device names is kept: media is often on 554 or 8554 while the device
// answers on 80).
func (s *soap) pin(raw string, schemes ...string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", errors.New("no usable address")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("not an address")
	}
	ok := false
	for _, sc := range schemes {
		if strings.EqualFold(u.Scheme, sc) {
			ok = true
			u.Scheme = sc
		}
	}
	if !ok {
		return "", errors.New("not a " + strings.Join(schemes, " or ") + " address")
	}
	u.User = nil
	host := s.dev.Hostname()
	port := u.Port()
	if u.Scheme == "http" || u.Scheme == "https" {
		// the credentials go to these: never to a weaker scheme than the manifest's (a
		// device reached over https often names its own services as http), and a device
		// behind a port forward that names no port is reached on the manifest's
		if strings.EqualFold(s.dev.Scheme, "https") {
			u.Scheme = "https"
		}
		if port == "" {
			port = s.dev.Port()
		}
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", errors.New("a port that is not one")
		}
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return u.String(), nil
}

// call sends a SOAP request with the camera's credentials and decodes the answer
// into out.
func (s *soap) call(ctx context.Context, to, body string, out any) error {
	cctx, cancel := context.WithTimeout(ctx, onvifTime)
	defer cancel()
	return s.send(cctx, to, body, out, true)
}

func (s *soap) send(ctx context.Context, to, body string, out any, sign bool) error {
	hdr := ""
	if sign && s.c.Credentials {
		user, pass, err := s.f.credentials(s.c)
		if err != nil {
			return err
		}
		h, err := wsse(user, pass, time.Now().Add(s.offset))
		if err != nil {
			return err
		}
		hdr = "<s:Header>" + h + "</s:Header>"
	}
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="` + nsSOAP + `">` + hdr + `<s:Body>` + body + `</s:Body></s:Envelope>`
	resp, err := s.f.request(ctx, s.c, http.MethodPost, to, []byte(env), `application/soap+xml; charset=utf-8`)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxSOAPBytes+1))
	if err != nil {
		return readErr(err)
	}
	if len(b) > MaxSOAPBytes {
		return fmt.Errorf("the device's answer is larger than %d bytes", MaxSOAPBytes)
	}
	return decodeSOAP(b, out)
}

// soapFault is a device saying no, as opposed to a device that did not answer.
type soapFault struct{ reason string }

func (e *soapFault) Error() string { return "the device refused: " + e.reason }

// decodeSOAP reads a SOAP answer: a fault is an error carrying the device's
// reason (bounded; it is the device's text), anything else fills out.
func decodeSOAP(b []byte, out any) error {
	var fault struct {
		Reason struct {
			Text string `xml:"Text"`
		} `xml:"Body>Fault>Reason"`
		Code string `xml:"Body>Fault>Code>Value"`
	}
	if err := xml.Unmarshal(b, &fault); err != nil {
		return errors.New("the device's answer is not SOAP")
	}
	if fault.Code != "" || fault.Reason.Text != "" {
		return &soapFault{reason: cleanText(fault.Reason.Text, 120)}
	}
	if out == nil {
		return nil
	}
	if err := xml.Unmarshal(b, out); err != nil {
		return errors.New("the device's answer could not be read")
	}
	return nil
}

// wsse is a WS-Security UsernameToken with a password digest:
// base64(sha1(nonce + created + password)).
func wsse(user, pass string, at time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	created := at.UTC().Format("2006-01-02T15:04:05Z")
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(pass))
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(user))
	return `<wsse:Security xmlns:wsse="` + nsWSSE + `" xmlns:wsu="` + nsWSU + `"><wsse:UsernameToken><wsse:Username>` + b.String() +
		`</wsse:Username><wsse:Password Type="` + pwDig + `">` + base64.StdEncoding.EncodeToString(h.Sum(nil)) +
		`</wsse:Password><wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` +
		base64.StdEncoding.EncodeToString(nonce) + `</wsse:Nonce><wsu:Created>` + created + `</wsu:Created></wsse:UsernameToken></wsse:Security>`, nil
}

// cleanText is device text made safe to show: no control characters, cut to n.
func cleanText(s string, n int) string {
	// controls (C0 and C1: U+009B is 8-bit CSI), format characters (bidi overrides,
	// zero-width), and line and paragraph separators: none of them may reach a terminal
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}
