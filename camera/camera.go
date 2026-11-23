// Package camera is the plant's cameras (#724): a camera is a row in the
// manifest bound to an asset, and the controller relays it, so a browser never
// talks to the camera and never sees its credentials.
//
// This package knows no server, auth, runtime or plant model: it holds the
// rows and their rules, fetches a snapshot, relays a motion-JPEG stream, draws
// the virtual camera and counts the viewers. The server mounts it (server/
// cameras.go) and the manifest composes it (internal/project). It is not in the
// playground's wasm: the browser build draws a virtual camera itself.
package camera

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Kinds of camera.
const (
	KindVirtual  = "virtual"  // drawn by the controller: demos, tests, the playground's twin
	KindSnapshot = "snapshot" // a JPEG at a URL, fetched on a timer by the browser
	KindMJPEG    = "mjpeg"    // a multipart motion-JPEG stream at a URL
	KindHLS      = "hls"      // an HLS playlist at a URL (a later step)
	KindRTSP     = "rtsp"     // RTSP, remuxed to HLS by the controller (a later step)
	KindONVIF    = "onvif"    // an ONVIF device: stream and snapshot found from it (a later step)
)

// Defaults and bounds.
const (
	DefaultIntervalSeconds = 5
	MinIntervalSeconds     = 1
	MaxIntervalSeconds     = 3600
	DefaultMaxViewers      = 2
	MaxMaxViewers          = 16
	// MaxLive is how many live streams one controller relays at once, across
	// every camera: a plant's radio link and a small ARM core both run out.
	MaxLive = 4
)

// Camera is one manifest row.
type Camera struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`
	// Asset is the plant equipment id the camera shows: the asset page lists
	// the cameras that name it.
	Asset string `yaml:"asset" json:"asset"`
	Kind  string `yaml:"kind" json:"kind"`
	// URL is the stream (mjpeg, hls, rtsp) or the device (onvif). Never
	// credentials in it: they are Secret's.
	URL string `yaml:"url" json:"-"`
	// Credentials says the camera needs a user name and password: the
	// controller reads them from the secrets file as SecretKey(id), set with
	// `naut alarms secrets set camera.<id>` ("user:password" on stdin). They
	// are never in the manifest, a URL, a log or an answer.
	Credentials bool `yaml:"credentials" json:"-"`
	// SnapshotURL is a JPEG at a URL; a snapshot camera's URL is this.
	SnapshotURL string `yaml:"snapshot-url" json:"-"`
	// IntervalSeconds is how often the asset panel refreshes the snapshot.
	IntervalSeconds int `yaml:"interval" json:"interval"`
	// MaxViewers is how many people may watch this camera live at once.
	MaxViewers int `yaml:"max-viewers" json:"maxViewers"`
	// SnapshotOnAlarm keeps a still when an alarm of the asset's equipment
	// activates; SnapshotAlarms adds alarm ids (of any equipment). Both need a
	// snapshot source. The pictures are kept under the state directory for the
	// camera-snapshots: policy, keyed by the alarm occurrence.
	SnapshotOnAlarm bool     `yaml:"snapshot-on-alarm" json:"-"`
	SnapshotAlarms  []string `yaml:"snapshot-alarms" json:"-"`
}

// SnapshotPolicy is the manifest's camera-snapshots: section: how long, how
// many and how many megabytes of alarm pictures are kept. They are pictures of
// people, so there is a bound on time as well as on space.
type SnapshotPolicy struct {
	KeepDays int `yaml:"keep-days"` // default 30
	MaxCount int `yaml:"max-count"` // default 500
	MaxMB    int `yaml:"max-mb"`    // default 200
}

// DefaultKeepDays is how long alarm pictures are kept unless the policy says.
const DefaultKeepDays = 30

// Validate checks the policy's bounds.
func (p *SnapshotPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.KeepDays < 0 || p.KeepDays > 3650 {
		return fmt.Errorf("camera-snapshots.keep-days is %d: between 1 and 3650", p.KeepDays)
	}
	if p.MaxCount < 0 || p.MaxCount > 100000 {
		return fmt.Errorf("camera-snapshots.max-count is %d: between 1 and 100000", p.MaxCount)
	}
	if p.MaxMB < 0 || p.MaxMB > 100000 {
		return fmt.Errorf("camera-snapshots.max-mb is %d: between 1 and 100000", p.MaxMB)
	}
	return nil
}

// Live reports whether the camera can be watched live (a stream, not only
// snapshots) in this build.
func (c Camera) Live() bool { return c.Kind == KindMJPEG || c.Kind == KindVirtual }

// HasSnapshot reports whether the controller can get a still from it.
func (c Camera) HasSnapshot() bool {
	switch c.Kind {
	case KindVirtual, KindSnapshot:
		return true
	case KindMJPEG:
		return true // a frame is cut from the stream
	}
	return c.SnapshotURL != ""
}

// Interval is the refresh in seconds, the default when unset.
func (c Camera) Interval() int {
	if c.IntervalSeconds == 0 {
		return DefaultIntervalSeconds
	}
	return c.IntervalSeconds
}

// Viewers is the live-viewer cap, the default when unset.
func (c Camera) Viewers() int {
	if c.MaxViewers == 0 {
		return DefaultMaxViewers
	}
	return c.MaxViewers
}

// Validate checks the rows on their own: unique ids, a known kind with what it
// needs, no credentials in a URL, bounds. What they name (an asset, a secret, a
// tag) is checked by the manifest, which knows those.
func Validate(cams []Camera) []error {
	var errs []error
	seen := map[string]bool{}
	for i, c := range cams {
		at := fmt.Sprintf("cameras[%d]", i)
		if c.ID != "" {
			at = fmt.Sprintf("camera %q", c.ID)
		}
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("%s: %s", at, fmt.Sprintf(format, a...)))
		}
		switch {
		case c.ID == "":
			bad("needs an id")
		case !idOK(c.ID):
			bad("the id may use letters, digits, - and _ (it is in a URL)")
		case seen[c.ID]:
			bad("another camera has this id")
		}
		seen[c.ID] = true
		if c.Asset == "" {
			bad("needs an asset: the plant.yaml equipment it shows")
		}
		switch c.Kind {
		case KindVirtual:
		case KindSnapshot:
			if c.URL == "" && c.SnapshotURL == "" {
				bad("kind snapshot needs a url: a JPEG")
			}
		case KindMJPEG, KindHLS, KindRTSP, KindONVIF:
			if c.URL == "" {
				bad("kind %s needs a url", c.Kind)
			}
		case "":
			bad("needs a kind (%s)", strings.Join(Kinds(), ", "))
		default:
			bad("kind %q is not one of %s", c.Kind, strings.Join(Kinds(), ", "))
		}
		for _, f := range []struct{ name, v string }{{"url", c.URL}, {"snapshot-url", c.SnapshotURL}} {
			if f.v == "" {
				continue
			}
			u, err := url.Parse(f.v)
			switch {
			case err != nil || u.Host == "":
				bad("%s is not a URL with a host", f.name)
			case queryCredentials(u):
				bad("%s holds a user name, password or token in its query: set credentials: true and put them in the secrets file (naut alarms secrets set camera.<id>)", f.name)
			case u.User != nil:
				bad("%s holds a user name or password: set credentials: true and put them in the secrets file (naut alarms secrets set camera.<id>)", f.name)
			case !schemeOK(c.Kind, f.name, u.Scheme):
				bad("%s: a %s camera's %s is %s", f.name, c.Kind, f.name, schemesFor(c.Kind, f.name))
			}
		}
		if c.IntervalSeconds != 0 && (c.IntervalSeconds < MinIntervalSeconds || c.IntervalSeconds > MaxIntervalSeconds) {
			bad("interval is %d seconds: between %d and %d", c.IntervalSeconds, MinIntervalSeconds, MaxIntervalSeconds)
		}
		if (c.SnapshotOnAlarm || len(c.SnapshotAlarms) > 0) && !c.HasSnapshot() {
			bad("snapshot-on-alarm needs a still: this kind has none, so name a snapshot-url")
		}
		for _, id := range c.SnapshotAlarms {
			if strings.TrimSpace(id) == "" {
				bad("snapshot-alarms holds an empty alarm id")
			}
		}
		if c.MaxViewers < 0 || c.MaxViewers > MaxMaxViewers {
			bad("max-viewers is %d: between 1 and %d", c.MaxViewers, MaxMaxViewers)
		}
	}
	return errs
}

// queryCredentials reports a URL whose query names a credential (user,
// password, token and their usual spellings): the manifest is committed, and the
// URL is logged by the camera's own server.
func queryCredentials(u *url.URL) bool {
	for k := range u.Query() {
		switch strings.ToLower(k) {
		case "user", "username", "usr", "login", "pass", "password", "passwd", "pwd", "token", "key", "apikey", "api_key", "auth", "secret":
			return true
		}
	}
	return false
}

// Kinds lists the kinds, for messages.
func Kinds() []string {
	return []string{KindVirtual, KindSnapshot, KindMJPEG, KindHLS, KindRTSP, KindONVIF}
}

func idOK(id string) bool {
	if len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func schemesFor(kind, field string) string {
	if kind == KindRTSP && field == "url" {
		return "rtsp:// or rtsps://"
	}
	return "http:// or https://"
}

func schemeOK(kind, field, scheme string) bool {
	scheme = strings.ToLower(scheme)
	if kind == KindRTSP && field == "url" {
		return scheme == "rtsp" || scheme == "rtsps"
	}
	return scheme == "http" || scheme == "https"
}

// SecretKey is the secrets-file key that holds camera id's "user:password".
func SecretKey(id string) string { return "camera." + id }

// SplitCredentials splits a secret's value, "user:password", into its parts.
func SplitCredentials(secret string) (user, pass string, err error) {
	user, pass, ok := strings.Cut(secret, ":")
	if !ok || user == "" {
		return "", "", errors.New("a camera secret is user:password")
	}
	if strings.ContainsAny(user, "\"\r\n") {
		return "", "", errors.New("a camera secret's user name may not hold a quote or a line break")
	}
	return user, pass, nil
}
