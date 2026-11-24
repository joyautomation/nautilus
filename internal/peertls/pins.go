package peertls

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// EventKind is what an Event reports.
type EventKind string

const (
	// EventPinned: a name's key was pinned at its first contact.
	EventPinned EventKind = "pinned"
	// EventRefused: a name's key is not the pinned one; refused.
	EventRefused EventKind = "refused"
	// EventDropped: a pin was dropped (naut redundancy unpin).
	EventDropped EventKind = "dropped"
	// EventPinsReset: a pins file could not be read: moved aside (Detail
	// says where) and every name of the cluster pins again on first
	// contact.
	EventPinsReset EventKind = "pins-reset"
	// EventPinFailed: a first contact's pin could not be written; the
	// contact was refused.
	EventPinFailed EventKind = "pin-failed"
	// EventNewKey: this box made its key (Detail says why: a first start,
	// a key written on another box, an unreadable key file).
	EventNewKey EventKind = "new-key"
)

// Event is a pin, a refusal, a drop, a reset pins file or a new box key:
// each goes to the journal (redundancy.peer-pin).
type Event struct {
	Kind    EventKind
	Cluster string // "" for the witness's name, or this box's own key
	Name    string
	// Fingerprint is the key's: the one pinned, refused, dropped or made.
	Fingerprint string
	// Pinned, on a refusal, is the pinned key's fingerprint.
	Pinned string
	// Addr is the other end's address, when known.
	Addr   string
	Detail string
	Time   time.Time
}

// pins are the keys pinned per cluster and name, each cluster's in its own
// file, read once and written through (atomically, synced) on a change.
type pins struct {
	path  func(cluster string) string
	event func(Event)
	now   func() time.Time

	mu    sync.Mutex
	byCl  map[string]map[string]ed25519.PublicKey
	write func(path string, b []byte) error // writeFileSync; a test's fault
	// dropped is, per cluster and name, the key whose pin was dropped:
	// never pinned again for that name (a connection kept from before the
	// drop must not re-pin it), until another key is pinned or this
	// process restarts.
	dropped map[string]map[string]ed25519.PublicKey
	// refusedAt dedupes refusal events per name and key: a refusal at the
	// handshake comes before the caller proved its key, so a replayed
	// certificate must not flood the journal.
	refusedAt map[string]time.Time
}

// refusalEvery is how often one name's refusal of one key is reported.
const refusalEvery = time.Minute

func newPins(path func(string) string, event func(Event), now func() time.Time) *pins {
	return &pins{path: path, event: event, now: now, byCl: map[string]map[string]ed25519.PublicKey{}, write: writeFileSync,
		dropped: map[string]map[string]ed25519.PublicKey{}, refusedAt: map[string]time.Time{}}
}

// pinsFile is a pins file's form.
type pinsFile struct {
	V    int               `json:"v"`
	Pins map[string]string `json:"pins"` // name -> base64 public key
}

// load reads cluster's pins (once). A missing file is no pins; an
// unreadable or unparsable one is moved aside, reported (EventPinsReset)
// and read as none: never silent, never an outage.
func (s *pins) load(cluster string) map[string]ed25519.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(cluster)
}

func (s *pins) loadLocked(cluster string) map[string]ed25519.PublicKey {
	if m, ok := s.byCl[cluster]; ok {
		return m
	}
	m := map[string]ed25519.PublicKey{}
	path := s.path(cluster)
	b, err := os.ReadFile(path)
	if err == nil {
		m, err = parsePins(b)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		m = map[string]ed25519.PublicKey{}
		if !mut("pins_unreadable_silent") {
			aside := path + ".unreadable-" + strconv.FormatInt(s.now().Unix(), 10)
			if rerr := os.Rename(path, aside); rerr != nil {
				aside = "not moved: " + rerr.Error()
			}
			s.event(Event{Kind: EventPinsReset, Cluster: cluster, Detail: fmt.Sprintf("the pins file %s could not be read (%v); kept at %s. Every member of the cluster pins again on its first contact: until the members reconnect, a token holder who answers first is pinned. Check the fingerprints (naut redundancy status on each box)", path, err, aside)})
		}
	}
	s.byCl[cluster] = m
	return m
}

func parsePins(b []byte) (map[string]ed25519.PublicKey, error) {
	var f pinsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.V != 1 {
		return nil, fmt.Errorf("pins file version %d, want 1", f.V)
	}
	m := map[string]ed25519.PublicKey{}
	for n, k := range f.Pins {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("pin of %q: not an Ed25519 key", n)
		}
		m[n] = ed25519.PublicKey(raw)
	}
	return m, nil
}

// saveLocked writes cluster's pins, durably.
func (s *pins) saveLocked(cluster string, m map[string]ed25519.PublicKey) error {
	f := pinsFile{V: 1, Pins: map[string]string{}}
	for n, k := range m {
		f.Pins[n] = base64.StdEncoding.EncodeToString(k)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return s.write(s.path(cluster), append(b, '\n'))
}

// match refuses key for name when name is pinned to another key. A name
// not pinned yet passes (check pins it once the handshake is complete).
func (s *pins) match(cluster, name string, key ed25519.PublicKey, addr string) error {
	s.mu.Lock()
	pinned, ok := s.loadLocked(cluster)[name]
	s.mu.Unlock()
	if !ok || equalKey(pinned, key) || mut("pin_accepts_changed_key") {
		return nil
	}
	return s.refused(cluster, name, pinned, key, addr)
}

// check is match after a complete handshake: a name not pinned yet is
// pinned to key now, written durably before the connection carries a
// request (a failed write refuses it: an unpinned contact never goes on).
func (s *pins) check(cluster, name string, key ed25519.PublicKey, addr string, dialed bool) error {
	s.mu.Lock()
	m := s.loadLocked(cluster)
	pinned, ok := m[name]
	if ok {
		s.mu.Unlock()
		if equalKey(pinned, key) || mut("pin_accepts_changed_key") {
			return nil
		}
		return s.refused(cluster, name, pinned, key, addr)
	}
	if d := s.dropped[cluster][name]; d != nil && equalKey(d, key) && !mut("drop_key_repins") {
		s.mu.Unlock()
		e := &IdentityError{Name: name, Dropped: true, Got: Fingerprint(key)}
		s.report(Event{Kind: EventRefused, Cluster: cluster, Name: name, Fingerprint: e.Got, Addr: addr, Detail: e.Error()})
		return e
	}
	next := make(map[string]ed25519.PublicKey, len(m)+1)
	for n, k := range m {
		next[n] = k
	}
	next[name] = key
	if err := s.saveLocked(cluster, next); err != nil {
		s.mu.Unlock()
		s.event(Event{Kind: EventPinFailed, Cluster: cluster, Name: name, Fingerprint: Fingerprint(key), Addr: addr,
			Detail: fmt.Sprintf("the first contact's pin could not be written (%v): the contact is refused until it can be", err)})
		return fmt.Errorf("pinning %s's key: %w", name, err)
	}
	s.byCl[cluster] = next
	delete(s.dropped[cluster], name)
	s.mu.Unlock()
	how := "it called this member"
	if dialed {
		how = "this member called it"
	}
	s.event(Event{Kind: EventPinned, Cluster: cluster, Name: name, Fingerprint: Fingerprint(key), Addr: addr,
		Detail: "first contact since nothing was pinned for it (" + how + "): its key is pinned; check the fingerprint against naut redundancy status on that box"})
	return nil
}

func (s *pins) refused(cluster, name string, pinned, key ed25519.PublicKey, addr string) error {
	e := &IdentityError{Name: name, Pinned: Fingerprint(pinned), Got: Fingerprint(key)}
	s.report(Event{Kind: EventRefused, Cluster: cluster, Name: name, Fingerprint: e.Got, Pinned: e.Pinned, Addr: addr, Detail: e.Error()})
	return e
}

// report sends a refusal's event, once a minute per name and key: a
// refusal at the handshake comes before the caller proved its key (a
// member's certificate is public), and a kept connection refuses at
// every request.
func (s *pins) report(e Event) {
	k := e.Cluster + "/" + e.Name + "/" + e.Fingerprint
	now := s.now()
	s.mu.Lock()
	last, seen := s.refusedAt[k]
	send := !seen || now.Sub(last) >= refusalEvery || mut("refusals_undeduped")
	if send {
		if len(s.refusedAt) > 1024 {
			clear(s.refusedAt) // bounded: a flood of keys reports again, never grows memory
		}
		s.refusedAt[k] = now
	}
	s.mu.Unlock()
	if send {
		s.event(e)
	}
}

// drop removes name's pin in cluster.
func (s *pins) drop(cluster, name, why string) error {
	s.mu.Lock()
	m := s.loadLocked(cluster)
	old, ok := m[name]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	next := make(map[string]ed25519.PublicKey, len(m))
	for n, k := range m {
		if n != name {
			next[n] = k
		}
	}
	if err := s.saveLocked(cluster, next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.byCl[cluster] = next
	if s.dropped[cluster] == nil {
		s.dropped[cluster] = map[string]ed25519.PublicKey{}
	}
	s.dropped[cluster][name] = old
	s.mu.Unlock()
	s.event(Event{Kind: EventDropped, Cluster: cluster, Name: name, Fingerprint: Fingerprint(old), Detail: why})
	return nil
}

// list is cluster's pins as fingerprints.
func (s *pins) list(cluster string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for n, k := range s.loadLocked(cluster) {
		out[n] = Fingerprint(k)
	}
	return out
}

// Names is a pins map's names, sorted.
func Names(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
