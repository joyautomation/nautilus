// Package oncall keeps what paging a person needs and the manifest must
// never hold (#715): each person's addresses (the contacts file) and the
// channels' credentials (the secrets file). Both live in the controller's
// state directory beside the users file, mode 0600, never in the project
// and never in Git; `naut alarms contacts` and `naut alarms secrets` write
// them on the box, and a running controller reads a change on its next
// send.
//
// A person is a principal, as the manifest's levels name them: a local
// user name (alice) or oidc:<name>. An address is never journalled or
// logged: a notify row names the principal and the channel kind only.
//
// It imports only the standard library and internal/securefile
// (docs/design/context-map.md §2).
package oncall

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/securefile"
)

// File names in the state directory.
const (
	ContactsName = "alarm-contacts.json"
	SecretsName  = "alarm-secrets.json"
	// LockName serializes naut alarms contacts and secrets runs.
	LockName = ".alarm-contacts.lock"
)

// The kinds a contact holds an address for.
const (
	Email = "email"
	SMS   = "sms"
	Voice = "voice"
)

// Kinds lists the kinds a contact may hold, in display order.
var Kinds = []string{Email, SMS, Voice}

const version = 1

// lockWait is how long an update waits for another run.
const lockWait = 30 * time.Second

// tool names what writes these files, for a refusal's fix.
const tool = "naut alarms contacts"

// geteuid is os.Geteuid, a variable so a test can be another user.
var geteuid = os.Geteuid

// ContactsPath and SecretsPath are the files' paths in a state directory.
func ContactsPath(stateDir string) string { return filepath.Join(stateDir, ContactsName) }
func SecretsPath(stateDir string) string  { return filepath.Join(stateDir, SecretsName) }

// Contact is one person's addresses. Empty means none for that kind.
type Contact struct {
	Email string `json:"email,omitempty"`
	SMS   string `json:"sms,omitempty"`
	Voice string `json:"voice,omitempty"`
}

// Address is c's address for kind, "" for none.
func (c *Contact) Address(kind string) string {
	if c == nil {
		return ""
	}
	switch kind {
	case Email:
		return c.Email
	case SMS:
		return c.SMS
	case Voice:
		return c.Voice
	}
	return ""
}

// Contacts is the contacts file.
type Contacts struct {
	Version   int                 `json:"version"`
	ChangedBy string              `json:"changedBy,omitempty"`
	ChangedAt time.Time           `json:"changedAt,omitzero"`
	Contacts  map[string]*Contact `json:"contacts"`
}

// Secrets is the secrets file: a credential by key (email.password,
// sms.token, …).
type Secrets struct {
	Version   int               `json:"version"`
	ChangedBy string            `json:"changedBy,omitempty"`
	ChangedAt time.Time         `json:"changedAt,omitzero"`
	Secrets   map[string]string `json:"secrets"`
}

// SecretKeys are the keys the secrets file may hold.
//
// push.vapid-key is the Web Push application server key (#722): the private
// half of the VAPID pair, 32 octets as unpadded base64url. The same key
// must be in both members' files: a phone's subscription is bound to it.
var SecretKeys = []string{"email.password", "sms.token", "voice.token", "webhook.header", "push.vapid-key"}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// ValidPhone checks an E.164 number: +, then 7 to 15 digits.
func ValidPhone(s string) error {
	if !e164.MatchString(s) {
		return fmt.Errorf("%q is not an E.164 number (+, the country code, then the number: +15550100)", s)
	}
	return nil
}

// ValidEmail checks a bare address (no display name).
func ValidEmail(s string) error {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 254 {
		return fmt.Errorf("%q is not a bare email address (name@example.com)", s)
	}
	return nil
}

// ValidPrincipal checks a person as a level names one: a user name or
// oidc:<name>, at most 128 characters, no spaces or control characters.
func ValidPrincipal(s string) error {
	name := s
	if rest, ok := strings.CutPrefix(s, "oidc:"); ok {
		name = rest
	} else if strings.Contains(s, ":") {
		return fmt.Errorf("%q is not a person: want a user name (alice) or oidc:<name>", s)
	}
	if name == "" || len(s) > 128 {
		return fmt.Errorf("%q is not a person: want a user name (alice) or oidc:<name>", s)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("%q is not a person: it holds a space or a control character", s)
		}
	}
	return nil
}

// Check validates one contact's addresses.
func (c *Contact) Check() error {
	if c.Email != "" {
		if err := ValidEmail(c.Email); err != nil {
			return err
		}
	}
	for _, p := range []string{c.SMS, c.Voice} {
		if p != "" {
			if err := ValidPhone(p); err != nil {
				return err
			}
		}
	}
	return nil
}

func validSecretKey(k string) bool {
	for _, s := range SecretKeys {
		if s == k {
			return true
		}
	}
	return false
}

// read decodes a file strictly: unknown fields, a second document and
// another version are refused. It checks the file as the users file is
// checked (securefile): mode, symbolic link, the directory's mode and,
// with owner, that this process owns it.
func read(path, noun, secret string, owner bool, v any, ver func() int) (bool, error) {
	if err := securefile.CheckMode(path, noun, secret); err != nil {
		return false, err
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner {
		if err := securefile.CheckOwner(path, st, geteuid(), tool); err != nil {
			return false, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return false, fmt.Errorf("%s: more than one JSON document", path)
	}
	if ver() != version {
		return false, fmt.Errorf("%s: version %d; this nautilus reads version %d", path, ver(), version)
	}
	return true, nil
}

// LoadContacts reads the contacts file at path; an absent one is empty.
// It refuses one others may read, a symbolic link, one in a directory
// others may write, and one this process does not own.
func LoadContacts(path string) (*Contacts, error) { return loadContacts(path, true) }

func loadContacts(path string, owner bool) (*Contacts, error) {
	var c Contacts
	ok, err := read(path, "the contacts file", "people's addresses", owner, &c, func() int { return c.Version })
	if err != nil {
		return nil, err
	}
	if !ok || c.Contacts == nil {
		c.Version, c.Contacts = version, map[string]*Contact{}
	}
	for p, ct := range c.Contacts {
		if err := ValidPrincipal(p); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if ct == nil {
			return nil, fmt.Errorf("%s: contact %q is null", path, p)
		}
		if err := ct.Check(); err != nil {
			return nil, fmt.Errorf("%s: contact %q: an address is malformed", path, p)
		}
	}
	return &c, nil
}

// LoadSecrets reads the secrets file at path; an absent one is empty.
// The same checks as LoadContacts.
func LoadSecrets(path string) (*Secrets, error) { return loadSecrets(path, true) }

func loadSecrets(path string, owner bool) (*Secrets, error) {
	var s Secrets
	ok, err := read(path, "the secrets file", "the channels' credentials", owner, &s, func() int { return s.Version })
	if err != nil {
		return nil, err
	}
	if !ok || s.Secrets == nil {
		s.Version, s.Secrets = version, map[string]string{}
	}
	for k, v := range s.Secrets {
		if !validSecretKey(k) {
			return nil, fmt.Errorf("%s: %q is not a secret key (%s)", path, k, strings.Join(SecretKeys, ", "))
		}
		if v == "" || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%s: secret %s is empty or holds a line break", path, k)
		}
	}
	return &s, nil
}

func save(path, pattern string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return securefile.WriteAtomic(path, pattern, append(b, '\n'), geteuid() == 0)
}

// locked runs fn under the lock in path's directory, after the mode
// checks on path.
func locked(path, noun, secret string, fn func() error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := securefile.Lock(filepath.Join(dir, LockName), geteuid() == 0, lockWait, tool)
	if err != nil {
		return err
	}
	defer unlock()
	if err := securefile.CheckMode(path, noun, secret); err != nil {
		return err
	}
	return fn()
}

// UpdateContacts changes the contacts file at path under its lock: it
// reads it, applies fn, checks it and saves it (by records who). Run as
// root (sudo), it does not require owning the file it replaces, and the
// new file keeps the old one's owner.
func UpdateContacts(path, by string, fn func(*Contacts) error) error {
	return locked(path, "the contacts file", "people's addresses", func() error {
		c, err := loadContacts(path, geteuid() != 0)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
		for p, ct := range c.Contacts {
			if err := ValidPrincipal(p); err != nil {
				return err
			}
			if err := ct.Check(); err != nil {
				return err
			}
			if *ct == (Contact{}) {
				delete(c.Contacts, p)
			}
		}
		c.Version, c.ChangedBy, c.ChangedAt = version, by, time.Now().UTC().Truncate(time.Second)
		return save(path, ".alarm-contacts-*.json", c)
	})
}

// UpdateSecrets changes the secrets file at path, as UpdateContacts does.
func UpdateSecrets(path, by string, fn func(*Secrets) error) error {
	return locked(path, "the secrets file", "the channels' credentials", func() error {
		s, err := loadSecrets(path, geteuid() != 0)
		if err != nil {
			return err
		}
		if err := fn(s); err != nil {
			return err
		}
		for k, v := range s.Secrets {
			if !validSecretKey(k) {
				return fmt.Errorf("%q is not a secret key (%s)", k, strings.Join(SecretKeys, ", "))
			}
			if v == "" || strings.ContainsAny(v, "\r\n") {
				return fmt.Errorf("secret %s is empty or holds a line break", k)
			}
		}
		s.Version, s.ChangedBy, s.ChangedAt = version, by, time.Now().UTC().Truncate(time.Second)
		return save(path, ".alarm-secrets-*.json", s)
	})
}

// Principals lists the contacts' principals, sorted.
func (c *Contacts) Principals() []string {
	out := make([]string, 0, len(c.Contacts))
	for p := range c.Contacts {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Store is the two files in a state directory as a running controller
// reads them: each lookup re-reads a file that changed since (its
// modification time, size, mode or identity), so a contact set on the
// box reaches the next send without a restart.
type Store struct {
	contacts, secrets string

	mu   sync.Mutex
	cst  stamp
	sst  stamp
	c    *Contacts
	s    *Secrets
	cerr error
	serr error
}

// Open reads the files in stateDir. An absent file is an empty one; one
// that fails its checks is an error here, and later a failed lookup.
func Open(stateDir string) (*Store, error) {
	s := &Store{contacts: ContactsPath(stateDir), secrets: SecretsPath(stateDir)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	return s, errors.Join(s.cerr, s.serr)
}

// Dir is the state directory the store reads.
func (s *Store) Dir() string { return filepath.Dir(s.contacts) }

// Address is principal's address for kind: "" when the person has none.
// An error means the contacts file cannot be read; it never holds an
// address.
func (s *Store) Address(principal, kind string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.cerr != nil {
		return "", s.cerr
	}
	return s.c.Lookup(principal).Address(kind), nil
}

// Lookup is principal's contact, nil for none. A local user name is
// stored lowercase (naut users, naut alarms contacts set) and a manifest
// may spell it otherwise, so it is looked up ignoring case; an oidc: name
// is exact.
func (c *Contacts) Lookup(principal string) *Contact {
	if c == nil {
		return nil
	}
	ct := c.Contacts[principal]
	if ct == nil && !strings.HasPrefix(principal, "oidc:") {
		ct = c.Contacts[strings.ToLower(principal)]
	}
	return ct
}

// Secret is the credential under key: "" when the file holds none.
func (s *Store) Secret(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.serr != nil {
		return "", s.serr
	}
	return s.s.Secrets[key], nil
}

// Contacts is the contacts file as last read (nil if it could not be).
func (s *Store) Contacts() (*Contacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	return s.c, s.cerr
}

func (s *Store) reload() {
	if st, err := stat(s.contacts); err != nil || !st.same(s.cst) || s.c == nil {
		s.cst = st
		s.c, s.cerr = LoadContacts(s.contacts)
		if err != nil {
			s.cerr = err
		}
	}
	if st, err := stat(s.secrets); err != nil || !st.same(s.sst) || s.s == nil {
		s.sst = st
		s.s, s.serr = LoadSecrets(s.secrets)
		if err != nil {
			s.serr = err
		}
	}
}

// stamp is what says a file changed: its modification time, size, mode
// and identity (an atomic save renames a new file over the old one, so a
// change within the clock's resolution is still seen).
type stamp struct {
	exists bool
	mod    time.Time
	size   int64
	mode   os.FileMode
	fi     os.FileInfo
}

func stat(path string) (stamp, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return stamp{}, nil
	}
	if err != nil {
		return stamp{}, err
	}
	return stamp{exists: true, mod: st.ModTime(), size: st.Size(), mode: st.Mode(), fi: st}, nil
}

func (a stamp) same(b stamp) bool {
	if a.exists != b.exists {
		return false
	}
	if !a.exists {
		return true
	}
	return a.mod.Equal(b.mod) && a.size == b.size && a.mode == b.mode && os.SameFile(a.fi, b.fi)
}

// ReadContacts and ReadSecrets read the files as LoadContacts does but do
// not require owning them: for a tool that only reports (naut check, run
// with sudo on the box, reads a file the service user owns).
func ReadContacts(path string) (*Contacts, error) { return loadContacts(path, false) }
func ReadSecrets(path string) (*Secrets, error)   { return loadSecrets(path, false) }
