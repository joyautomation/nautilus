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
	"strings"
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
	// EventDropped: a pin was dropped (naut redundancy unpin, or a
	// witness incarnation the pair no longer counts).
	EventDropped EventKind = "dropped"
	// EventBanLifted: the refusal of a dropped pin's key was lifted (a
	// second naut redundancy unpin of the name).
	EventBanLifted EventKind = "ban-lifted"
	// EventWitnessAllowed: an operator allowed the next new witness key
	// (naut redundancy unpin witness).
	EventWitnessAllowed EventKind = "witness-allowed"
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

// MaxWitnessNames is how many witness incarnations a controller pins
// besides the one its pair counts (#710 2b). A witness makes a new one
// only when it is rebuilt, and an adoption drops the others, so more is
// something else holding the peer token: a new name past it is refused,
// never one evicted (an evicted name would let a token holder take the
// incarnation the pair is about to adopt).
const MaxWitnessNames = 8

// clusterPins are one cluster's pins, as its file keeps them.
type clusterPins struct {
	keys map[string]ed25519.PublicKey
	// dropped is, per name, the key whose pin was dropped (naut
	// redundancy unpin): never pinned again for that name (a box still
	// holding it, or a connection kept from before the drop, must not
	// re-pin it), until another key is pinned for the name or a second
	// unpin lifts it. Kept in the file: a restart keeps the refusal.
	dropped map[string]ed25519.PublicKey
	// since is, per name, the count of drops when its key was pinned: a
	// name pinned after a drop is newer than one pinned before it. The
	// witness takes an unpin only from a member no newer than the one it
	// drops (cmd/naut).
	since map[string]uint64
	// drops counts the drops in this cluster.
	drops uint64
	// pending is, per name, the fingerprint of the key an unpin dropped
	// whose witness leg is not confirmed yet (cmd/naut): a retry finishes
	// it, about that key, and never lifts the refusal.
	pending map[string]string
	// allowWitness is the fingerprint of the new witness key an operator
	// allowed here (naut redundancy unpin witness), once, until it pins
	// or the pair adopts an incarnation.
	allowWitness string
	// witnessKey is the key of the witness incarnation this controller
	// counts (Promote), kept across adoptions: a new incarnation pins by
	// itself only under it, the counted one's name included (a name
	// adopted on the peer's proposal, never heard here, is no exception).
	witnessKey ed25519.PublicKey
}

func newClusterPins() *clusterPins {
	return &clusterPins{keys: map[string]ed25519.PublicKey{}, dropped: map[string]ed25519.PublicKey{}, since: map[string]uint64{}, pending: map[string]string{}}
}

func (c *clusterPins) clone() *clusterPins {
	n := newClusterPins()
	for k, v := range c.keys {
		n.keys[k] = v
	}
	for k, v := range c.dropped {
		n.dropped[k] = v
	}
	for k, v := range c.since {
		n.since[k] = v
	}
	for k, v := range c.pending {
		n.pending[k] = v
	}
	n.drops, n.allowWitness, n.witnessKey = c.drops, c.allowWitness, c.witnessKey
	return n
}

// pins are the keys pinned per cluster and name, each cluster's in its own
// file, read once and written through (atomically, synced) on a change.
type pins struct {
	path  func(cluster string) string
	event func(Event)
	now   func() time.Time
	// counted is the witness incarnation this controller's pair counts
	// (Options.Counted): its name is not one of the MaxWitnessNames.
	counted func() uint64

	mu    sync.Mutex
	byCl  map[string]*clusterPins
	write func(path string, b []byte) error // writeFileSync; a test's fault
	// refusedAt dedupes refusal events per name and key: a refusal at the
	// handshake comes before the caller proved its key, so a replayed
	// certificate must not flood the journal.
	refusedAt map[string]time.Time
	// waiting is, per cluster, the last new witness key refused for want
	// of an operator's allowance (naut redundancy status shows it).
	waiting map[string]ed25519.PublicKey
}

// refusalEvery is how often one name's refusal of one key is reported.
const refusalEvery = time.Minute

func newPins(path func(string) string, event func(Event), now func() time.Time, counted func() uint64) *pins {
	if counted == nil {
		counted = func() uint64 { return 0 }
	}
	return &pins{path: path, event: event, now: now, counted: counted, byCl: map[string]*clusterPins{}, write: writeFileSync,
		refusedAt: map[string]time.Time{}, waiting: map[string]ed25519.PublicKey{}}
}

// pinsFile is a pins file's form.
type pinsFile struct {
	V    int               `json:"v"`
	Pins map[string]string `json:"pins"` // name -> base64 public key
	// Dropped: name -> the base64 key whose pin was dropped (refused).
	Dropped map[string]string `json:"dropped,omitempty"`
	Since   map[string]uint64 `json:"since,omitempty"`
	Drops   uint64            `json:"drops,omitempty"`
	// Pending: name -> the fingerprint of the key an unpin dropped whose
	// witness leg is not confirmed yet.
	Pending map[string]string `json:"pending,omitempty"`
	// AllowWitness: the fingerprint of the new witness key allowed, once.
	AllowWitness string `json:"allowWitness,omitempty"`
	// WitnessKey: the base64 key of the counted witness incarnation.
	WitnessKey string `json:"witnessKey,omitempty"`
}

// load reads cluster's pins (once). A missing file is no pins; an
// unreadable or unparsable one is moved aside, reported (EventPinsReset)
// and read as none: never silent, never an outage.
func (s *pins) load(cluster string) *clusterPins {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(cluster)
}

func (s *pins) loadLocked(cluster string) *clusterPins {
	if m, ok := s.byCl[cluster]; ok {
		return m
	}
	m := newClusterPins()
	path := s.path(cluster)
	b, err := os.ReadFile(path)
	if err == nil {
		m, err = parsePins(b)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		m = newClusterPins()
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

func parsePins(b []byte) (*clusterPins, error) {
	var f pinsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.V != 1 {
		return nil, fmt.Errorf("pins file version %d, want 1", f.V)
	}
	m := newClusterPins()
	read := func(into map[string]ed25519.PublicKey, from map[string]string) error {
		for n, k := range from {
			raw, err := base64.StdEncoding.DecodeString(k)
			if err != nil || len(raw) != ed25519.PublicKeySize {
				return fmt.Errorf("pin of %q: not an Ed25519 key", n)
			}
			if n == Witness {
				// A pre-#710-2b pin of the one witness name (dev and
				// bench boxes only: 2a was never released). The witness
				// now names its incarnation, pinned at first contact.
				continue
			}
			into[n] = ed25519.PublicKey(raw)
		}
		return nil
	}
	if err := read(m.keys, f.Pins); err != nil {
		return nil, err
	}
	if err := read(m.dropped, f.Dropped); err != nil {
		return nil, err
	}
	for n, v := range f.Since {
		m.since[n] = v
	}
	m.drops, m.allowWitness = f.Drops, f.AllowWitness
	if f.WitnessKey != "" {
		raw, err := base64.StdEncoding.DecodeString(f.WitnessKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("the counted witness key: not an Ed25519 key")
		}
		m.witnessKey = ed25519.PublicKey(raw)
	}
	for n, k := range f.Pending {
		m.pending[n] = k
	}
	return m, nil
}

// saveLocked writes cluster's pins, durably.
func (s *pins) saveLocked(cluster string, m *clusterPins) error {
	f := pinsFile{V: 1, Pins: map[string]string{}, Since: map[string]uint64{}, Drops: m.drops, AllowWitness: m.allowWitness}
	if m.witnessKey != nil {
		f.WitnessKey = base64.StdEncoding.EncodeToString(m.witnessKey)
	}
	if len(m.pending) > 0 {
		f.Pending = map[string]string{}
		for n, k := range m.pending {
			f.Pending[n] = k
		}
	}
	for n, k := range m.keys {
		f.Pins[n] = base64.StdEncoding.EncodeToString(k)
	}
	if len(m.dropped) > 0 && !mut("drop_ban_in_memory") {
		f.Dropped = map[string]string{}
		for n, k := range m.dropped {
			f.Dropped[n] = base64.StdEncoding.EncodeToString(k)
		}
	}
	for n, v := range m.since {
		if v != 0 {
			f.Since[n] = v
		}
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
	pinned, ok := s.loadLocked(cluster).keys[name]
	s.mu.Unlock()
	if !ok || equalKey(pinned, key) || mut("pin_accepts_changed_key") {
		return nil
	}
	return s.refused(cluster, name, pinned, key, addr)
}

// witnessNames counts m's witness incarnation names other than counted's.
func witnessNames(m *clusterPins, counted uint64) int {
	n := 0
	for name := range m.keys {
		if inc, ok := witnessInc(name); ok && inc != counted {
			n++
		}
	}
	return n
}

// check is match after a complete handshake: a name not pinned yet is
// pinned to key now, written durably before the connection carries a
// request (a failed write refuses it: an unpinned contact never goes on).
func (s *pins) check(cluster, name string, key ed25519.PublicKey, addr string, dialed bool) error {
	s.mu.Lock()
	m := s.loadLocked(cluster)
	pinned, ok := m.keys[name]
	if ok {
		s.mu.Unlock()
		if equalKey(pinned, key) || mut("pin_accepts_changed_key") {
			return nil
		}
		return s.refused(cluster, name, pinned, key, addr)
	}
	if d := m.dropped[name]; d != nil && equalKey(d, key) && !mut("drop_key_repins") {
		s.mu.Unlock()
		e := &IdentityError{Name: name, Dropped: true, Got: Fingerprint(key)}
		s.report(Event{Kind: EventRefused, Cluster: cluster, Name: name, Fingerprint: e.Got, Addr: addr, Detail: e.Error()})
		return e
	}
	inc, isWit := witnessInc(name)
	counted := s.counted()
	allowed := false
	if isWit && !mut("witness_any_new_key") {
		// A witness incarnation pins by itself only under the key of the
		// one this controller counts (a rebuild keeps the witness's box
		// key), kept across adoptions (witnessKey): the counted name too,
		// since one adopted on the peer's proposal was never heard here.
		// Another key is a new witness box, or a token holder at the
		// witness's address: refused until an operator allows that very
		// key here (#710 2b B'). With no key known (a new pair, or this
		// controller's pins lost), the first contact pins, as any name's;
		// a commissioned one then takes only its counted name.
		ck := m.witnessKey
		if mut("witness_key_forgotten") {
			ck = nil
		}
		if ck == nil && counted != 0 {
			ck = m.keys[WitnessName(counted)]
		}
		switch {
		case ck == nil && (counted == 0 || inc == counted || mut("unknown_key_any_name")):
		case ck != nil && equalKey(ck, key):
		case m.allowWitness != "" && (m.allowWitness == Fingerprint(key) || mut("allowance_any_key")):
			allowed = true
		default:
			s.waiting[cluster] = key
			s.mu.Unlock()
			e := &IdentityError{Name: name, NewWitness: true, Got: Fingerprint(key)}
			if ck != nil {
				e.Pinned = Fingerprint(ck)
			}
			s.report(Event{Kind: EventRefused, Cluster: cluster, Name: name, Fingerprint: e.Got, Pinned: e.Pinned, Addr: addr, Detail: e.Error()})
			return e
		}
	}
	if isWit && inc != counted && witnessNames(m, counted) >= MaxWitnessNames && !mut("witness_names_uncapped") {
		s.mu.Unlock()
		e := &IdentityError{Name: name, Full: true}
		s.report(Event{Kind: EventRefused, Cluster: cluster, Name: name, Fingerprint: Fingerprint(key), Addr: addr, Detail: e.Error()})
		return e
	}
	next := m.clone()
	if allowed {
		next.allowWitness = "" // once
	}
	if isWit && counted != 0 && inc == counted {
		next.witnessKey = key // the counted incarnation's key, from here on
	}
	next.keys[name] = key
	next.since[name] = m.drops
	delete(next.dropped, name)
	if err := s.saveLocked(cluster, next); err != nil {
		s.mu.Unlock()
		s.event(Event{Kind: EventPinFailed, Cluster: cluster, Name: name, Fingerprint: Fingerprint(key), Addr: addr,
			Detail: fmt.Sprintf("the first contact's pin could not be written (%v): the contact is refused until it can be", err)})
		return fmt.Errorf("pinning %s's key: %w", name, err)
	}
	s.byCl[cluster] = next
	if isWit {
		delete(s.waiting, cluster)
	}
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

// DropOutcome is what a drop found.
type DropOutcome string

const (
	// Dropped: the name was pinned; its pin is gone and its key refused.
	Dropped DropOutcome = "dropped"
	// AlreadyDropped: the name is not pinned, and the key of its last
	// dropped pin is refused (an earlier unpin).
	AlreadyDropped DropOutcome = "already-dropped"
	// NotPinned: the name is not pinned and no key of it is refused.
	NotPinned DropOutcome = "not-pinned"
	// OtherKey: the name holds another key than the one asked about
	// (pinned, or refused, since): nothing changed.
	OtherKey DropOutcome = "other-key"
)

// update changes cluster's pins under the lock with f, and writes them
// when f reports a change.
func (s *pins) update(cluster string, f func(*clusterPins) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.loadLocked(cluster).clone()
	if !f(next) {
		return nil
	}
	if err := s.saveLocked(cluster, next); err != nil {
		return err
	}
	s.byCl[cluster] = next
	return nil
}

// drop removes name's pin in cluster; ban: its key is refused from then
// on (an unpin), else it may pin again (an incarnation the pair no longer
// counts).
func (s *pins) drop(cluster, name, why string, ban bool, expect string) (DropOutcome, error) {
	s.mu.Lock()
	m := s.loadLocked(cluster)
	old, ok := m.keys[name]
	if expect != "" && !mut("unpin_ignores_key") {
		// Only that key: one pinned (or refused) since stays (the witness's
		// side of an unpin, checked under the lock).
		held := old
		if !ok {
			held = m.dropped[name]
		}
		if held != nil && Fingerprint(held) != expect {
			s.mu.Unlock()
			return OtherKey, nil
		}
	}
	if !ok {
		out := NotPinned
		if m.dropped[name] != nil {
			out = AlreadyDropped
		}
		s.mu.Unlock()
		return out, nil
	}
	next := m.clone()
	delete(next.keys, name)
	if ban {
		next.dropped[name] = old
		next.drops++
	}
	if err := s.saveLocked(cluster, next); err != nil {
		s.mu.Unlock()
		return "", err
	}
	s.byCl[cluster] = next
	s.mu.Unlock()
	s.event(Event{Kind: EventDropped, Cluster: cluster, Name: name, Fingerprint: Fingerprint(old), Detail: why})
	return Dropped, nil
}

// lift ends the refusal of name's dropped key: the next key that contacts
// as name pins. It reports whether one was refused.
func (s *pins) lift(cluster, name, why, expect string) (bool, error) {
	s.mu.Lock()
	m := s.loadLocked(cluster)
	old := m.dropped[name]
	if old == nil || expect != "" && Fingerprint(old) != expect && !mut("unpin_ignores_key") {
		s.mu.Unlock()
		return false, nil
	}
	next := m.clone()
	delete(next.dropped, name)
	if err := s.saveLocked(cluster, next); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.byCl[cluster] = next
	s.mu.Unlock()
	s.event(Event{Kind: EventBanLifted, Cluster: cluster, Name: name, Fingerprint: Fingerprint(old), Detail: why})
	return true, nil
}

// since is name's pin epoch in cluster, and whether name is pinned or
// was (a dropped pin keeps its epoch).
func (s *pins) sinceOf(cluster, name string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.loadLocked(cluster)
	v, ok := m.since[name]
	_, pinned := m.keys[name]
	_, dropped := m.dropped[name]
	return v, ok || pinned || dropped
}

// list is cluster's pins as fingerprints.
func (s *pins) list(cluster string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for n, k := range s.loadLocked(cluster).keys {
		out[n] = Fingerprint(k)
	}
	return out
}

// droppedList is cluster's refused dropped keys as fingerprints.
func (s *pins) droppedList(cluster string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for n, k := range s.loadLocked(cluster).dropped {
		out[n] = Fingerprint(k)
	}
	return out
}

// witnessPins are cluster's pinned witness incarnation names.
func (s *pins) witnessPins(cluster string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.loadLocked(cluster).keys {
		if _, ok := witnessInc(n); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
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

// witnessInc reads a witness incarnation name, "witness@<inc>" (decimal,
// as WitnessName writes it).
func witnessInc(name string) (uint64, bool) {
	s, ok := strings.CutPrefix(name, Witness+"@")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s {
		return 0, false
	}
	return n, true
}

// DropFile drops name's pin in the pins file at path and refuses its key
// from then on, as Drop does, for a member that is stopped (naut witness
// unpin, with the witness down). A file that can't be read is an error,
// left as it is.
func DropFile(path, name, why string) (DropOutcome, error) {
	s, err := fileStore(path)
	if err != nil || s == nil {
		return NotPinned, err
	}
	return s.drop("", name, why, true, "")
}

// LiftFile lifts the refusal of name's dropped key in the pins file at
// path (a second naut witness unpin), reporting whether one was refused.
func LiftFile(path, name, why string) (bool, error) {
	s, err := fileStore(path)
	if err != nil || s == nil {
		return false, err
	}
	return s.lift("", name, why, "")
}

// fileStore is a store over the one pins file at path; nil when there is
// none.
func fileStore(path string) (*pins, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := parsePins(b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return newPins(func(string) string { return path }, func(Event) {}, time.Now, nil), nil
}
