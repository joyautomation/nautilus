package users

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The two files beside the users file that OIDC sign-in and naut users
// signout add (#708 PR 6a). They are files of their own, not fields of
// users.json: users.json is read strictly, and a field an older nautilus
// does not know would make it refuse the whole file.
const (
	// PinsName holds each OIDC name's pin: the account at the provider,
	// (issuer, subject), that the name was first signed in as. The
	// controller writes it (trust on first use); naut users unpin removes
	// a pin.
	PinsName = "oidc-pins.json"
	// SignoutsName holds when each principal was last signed out by naut
	// users signout: a browser session started before it acts no more.
	SignoutsName = "signouts.json"
)

// maxPinName bounds a pinned name (auth.MaxOIDCName; auth is not imported
// here).
const maxPinName = 128

// Pins is the OIDC pins file.
type Pins struct {
	Version   int            `json:"version"`
	ChangedBy string         `json:"changedBy,omitempty"`
	ChangedAt time.Time      `json:"changedAt,omitzero"`
	Pins      map[string]Pin `json:"pins"`
}

// Pin is the account an OIDC name belongs to.
type Pin struct {
	Issuer  string    `json:"issuer"`
	Subject string    `json:"subject"`
	Pinned  time.Time `json:"pinned"`
}

// Same reports whether p names the account (issuer, subject).
func (p Pin) Same(issuer, subject string) bool { return p.Issuer == issuer && p.Subject == subject }

// Signouts is the signouts file: by principal ("user:alice",
// "oidc:jsmith@corp.example"), the moment its sessions were ended.
type Signouts struct {
	Version   int                  `json:"version"`
	ChangedBy string               `json:"changedBy,omitempty"`
	ChangedAt time.Time            `json:"changedAt,omitzero"`
	Signouts  map[string]time.Time `json:"signouts"`
}

// signoutKeep is how long a sign-out is kept: past the longest session
// (auth.MaxSession, 7 days) it can end nothing.
const signoutKeep = 8 * 24 * time.Hour

// sideName checks a name in one of the side files: a pinned OIDC name or
// a signed-out principal. auth checks the form when it signs someone in;
// here it is enough that nothing odd reaches the journal.
func sideName(name string) error {
	if name == "" || len(name) > maxPinName+len("oidc:") {
		return fmt.Errorf("a name that is empty or too long")
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r <= ' ' || r == 0x7f || r > 0x7e }) {
		return fmt.Errorf("name %q holds a space, a control character or a non-ASCII character", name)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("name %q is not lowercase", name)
	}
	return nil
}

// readStrict decodes a side file: unknown fields, a second document and
// another version are refused, as the users file's are. An absent file is
// (nil, nil).
func readStrict(path string, v any, ver func() int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return fmt.Errorf("%s: more than one JSON document", path)
	}
	if ver() != version {
		return fmt.Errorf("%s: version %d; this nautilus reads version %d", path, ver(), version)
	}
	return nil
}

// LoadPins reads the pins file at path; an absent one is empty.
func LoadPins(path string) (*Pins, error) {
	var p Pins
	err := readStrict(path, &p, func() int { return p.Version })
	if errors.Is(err, os.ErrNotExist) {
		return &Pins{Version: version, Pins: map[string]Pin{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Pins == nil {
		p.Pins = map[string]Pin{}
	}
	for n, pin := range p.Pins {
		if err := sideName(n); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if pin.Issuer == "" || pin.Subject == "" {
			return nil, fmt.Errorf("%s: the pin of %q names no issuer or no subject", path, n)
		}
	}
	return &p, nil
}

// LoadSignouts reads the signouts file at path; an absent one is empty.
func LoadSignouts(path string) (*Signouts, error) {
	var s Signouts
	err := readStrict(path, &s, func() int { return s.Version })
	if errors.Is(err, os.ErrNotExist) {
		return &Signouts{Version: version, Signouts: map[string]time.Time{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if s.Signouts == nil {
		s.Signouts = map[string]time.Time{}
	}
	for n := range s.Signouts {
		if err := sideName(n); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !strings.HasPrefix(n, "user:") && !strings.HasPrefix(n, "oidc:") {
			return nil, fmt.Errorf("%s: %q is not a user:<name> or oidc:<name> principal", path, n)
		}
	}
	return &s, nil
}

// saveJSON writes v to path as the users file is written: atomically,
// mode 0600, the old file's owner kept.
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// locked runs fn under the users lock in dir (the one naut users takes),
// after the mode checks on path.
func locked(dir, path string, fn func() error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(dir, LockName))
	if err != nil {
		return err
	}
	defer unlock()
	if err := CheckMode(path); err != nil {
		return err
	}
	return fn()
}

// UpdatePins changes the pins file at path under the users lock: it reads
// it, applies fn and saves it (by records who).
func UpdatePins(path, by string, fn func(*Pins) error) error {
	return locked(filepath.Dir(path), path, func() error {
		p, err := LoadPins(path)
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
		p.Version, p.ChangedBy, p.ChangedAt = version, by, time.Now().UTC().Truncate(time.Second)
		return saveJSON(path, p)
	})
}

// ErrNotPinned is Unpin's error for a name with no pin.
var ErrNotPinned = errors.New("no pin")

// Unpin removes name's pin: its next sign-in pins it again, to whatever
// account it comes from.
func (p *Pins) Unpin(name string) error {
	if _, ok := p.Pins[name]; !ok {
		return fmt.Errorf("oidc:%s: %w", name, ErrNotPinned)
	}
	delete(p.Pins, name)
	return nil
}

// Names are the pinned names, sorted.
func (p *Pins) Names() []string {
	out := make([]string, 0, len(p.Pins))
	for n := range p.Pins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SignOut records that principal's sessions end at now, under the users
// lock, dropping sign-outs too old to end anything.
func SignOut(path, by, principal string, now time.Time) error {
	if err := sideName(principal); err != nil {
		return err
	}
	return locked(filepath.Dir(path), path, func() error {
		s, err := LoadSignouts(path)
		if err != nil {
			return err
		}
		for n, at := range s.Signouts {
			if now.Sub(at) > signoutKeep {
				delete(s.Signouts, n)
			}
		}
		s.Signouts[principal] = now.UTC()
		s.Version, s.ChangedBy, s.ChangedAt = version, by, now.UTC().Truncate(time.Second)
		return saveJSON(path, s)
	})
}

// diffPins is what changed between two pins files, for the journal: a
// pin added, removed or changed. The controller's own pins are journalled
// by the sign-in that made them, and it moves its baseline past them
// (Store.PinFirst), so what is told here is someone else's change.
func diffPins(old, new *Pins) []Change {
	if old == nil {
		old = &Pins{}
	}
	if new == nil {
		new = &Pins{}
	}
	names := map[string]bool{}
	for n := range old.Pins {
		names[n] = true
	}
	for n := range new.Pins {
		names[n] = true
	}
	var out []Change
	for _, n := range sortedKeys(names) {
		o, had := old.Pins[n]
		nu, has := new.Pins[n]
		var what string
		switch {
		case had && !has:
			what = "unpinned"
		case !had && has:
			what = "pinned"
		case !o.Same(nu.Issuer, nu.Subject):
			what = "pin changed"
		default:
			continue
		}
		out = append(out, Change{User: n, Principal: "oidc:" + n, What: what})
	}
	return out
}

// diffSignouts is the sign-outs new records that old did not.
func diffSignouts(old, new *Signouts) []Change {
	var out []Change
	if new == nil {
		return nil
	}
	var names []string
	for n := range new.Signouts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if old != nil {
			if at, ok := old.Signouts[n]; ok && at.Equal(new.Signouts[n]) {
				continue
			}
		}
		_, user, _ := strings.Cut(n, ":")
		out = append(out, Change{User: user, Principal: n, What: "signed out"})
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
