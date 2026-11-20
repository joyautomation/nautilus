// Package users is the users file on a controller (#708): each local
// user's argon2id password hash, whether the user is disabled, and the
// hashes of the user's personal access tokens. It lives in the
// controller's state directory, never in the project, so no hash reaches
// git; what a user may do is the manifest's auth: block (package auth).
//
// It imports only the standard library and golang.org/x/crypto
// (docs/design/context-map.md §2).
package users

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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

// FileName is the users file's name in the state directory.
const FileName = "users.json"

// version is the file format's version.
const version = 1

// MinPassword is the shortest password accepted.
const MinPassword = 8

// File is the users file.
type File struct {
	Version int `json:"version"`
	// ChangedBy and ChangedAt are who last changed the file (the
	// operating-system user naut users ran as) and when: the controller
	// journals each change by that name.
	ChangedBy string           `json:"changedBy,omitempty"`
	ChangedAt time.Time        `json:"changedAt,omitzero"`
	Users     map[string]*User `json:"users"`
}

// User is one local user's credentials.
type User struct {
	Password string  `json:"password"` // argon2id, PHC format
	Disabled bool    `json:"disabled,omitempty"`
	Tokens   []Token `json:"tokens,omitempty"`
}

// Token is a personal access token, stored as the SHA-256 of its secret
// (256 random bits, so no slow hash is needed).
type Token struct {
	ID      string    `json:"id"`
	Hash    string    `json:"hash"` // hex SHA-256 of the secret
	Created time.Time `json:"created"`
}

// Errors Authenticate returns. A caller shows all of them the same way
// (no account probing) and journals which it was.
var (
	ErrNoUser        = errors.New("no such user")
	ErrDisabled      = errors.New("the user is disabled")
	ErrWrongPassword = errors.New("wrong password")
)

// New is an empty users file.
func New() *File { return &File{Version: version, Users: map[string]*User{}} }

// ValidName checks a user name: 1 to 64 lowercase letters, digits, '-',
// '_' or '.', starting with a letter or digit. Names are stored lowercase
// (the auth: block matches names ignoring case).
func ValidName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("a user name is 1 to 64 characters")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return fmt.Errorf("user name %q: use lowercase letters, digits, '-', '_' and '.', starting with a letter or digit", name)
		}
	}
	return nil
}

// Load reads the users file at path. An absent file is an empty one: no
// local users yet.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	return parse(b, path)
}

func parse(b []byte, path string) (*File, error) {
	var f File
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return nil, fmt.Errorf("%s: more than one JSON document", path)
	}
	if f.Version != version {
		return nil, fmt.Errorf("%s: version %d; this nautilus reads version %d", path, f.Version, version)
	}
	if f.Users == nil {
		f.Users = map[string]*User{}
	}
	for name, u := range f.Users {
		if err := ValidName(name); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if u == nil {
			return nil, fmt.Errorf("%s: user %q is null", path, name)
		}
		if _, err := parsePHC(u.Password); err != nil {
			return nil, fmt.Errorf("%s: user %q: password: %w", path, name, err)
		}
		for _, t := range u.Tokens {
			if !validTokenID(t.ID) || len(t.Hash) != 2*sha256.Size {
				return nil, fmt.Errorf("%s: user %q: a token with a malformed id or hash", path, name)
			}
		}
	}
	return &f, nil
}

// Save writes f to path atomically (a temporary file, fsynced, renamed
// over the old one, the directory fsynced), mode 0600, recording by as
// the operating-system user who changed it. The directory is made, 0700,
// when it does not exist. Run as root, the file keeps the owner of the one
// it replaces (or of its directory), so the controller's service user can
// still read it.
func (f *File) Save(path, by string) error {
	f.Version, f.ChangedBy, f.ChangedAt = version, by, time.Now().UTC().Truncate(time.Second)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// writeAtomic writes b to path: a temporary file, mode 0600, owned as
// keepOwner says, fsynced, renamed over the old one, the directory
// fsynced. A path that is a symbolic link is refused.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: it must be a plain file", path)
	}
	tmp, err := os.CreateTemp(dir, ".users-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := keepOwner(tmp, path, dir); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// CheckMode refuses a users file that anyone but its owner may read or
// write (its hashes are what a password guess is tested against
// offline), one that is a symbolic link, and one in a directory others
// may write to (they could replace it). An absent file passes the file's
// checks. On Windows, where Go reports every file 0666 and access is the
// ACL's, the mode bits say nothing and are not checked.
func CheckMode(path string) error {
	if st, err := os.Stat(filepath.Dir(path)); err == nil && othersMayWrite(st.Mode().Perm()) {
		return fmt.Errorf("%s is mode %04o: other users can replace the users file in it; chmod go-w it", filepath.Dir(path), st.Mode().Perm())
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: the users file must be a plain file", path)
	}
	if m := st.Mode().Perm(); othersMayRead(m) {
		return fmt.Errorf("%s is mode %04o: other users can read the password hashes; chmod 600 it", path, m)
	}
	return nil
}

// LockName is the lock file beside the users file that serializes naut
// users runs.
const LockName = ".users.lock"

// lockWait is how long Update waits for another naut users.
const lockWait = 30 * time.Second

// Update changes the users file at path under its lock: it waits for any
// other naut users, reads the file, applies fn, and saves it (by records
// who). A run that waits at a password prompt asks first and takes the
// lock only for the change, so two runs never save over each other.
func Update(path, by string, fn func(*File) error) error {
	unlock, err := lockFile(filepath.Join(filepath.Dir(path), LockName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			unlock, err = lockFile(filepath.Join(filepath.Dir(path), LockName))
		}
		if err != nil {
			return err
		}
	}
	defer unlock()
	if err := CheckMode(path); err != nil {
		return err
	}
	f, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return f.Save(path, by)
}

func checkPassword(pw string) error {
	if len([]rune(pw)) < MinPassword {
		return fmt.Errorf("a password is at least %d characters", MinPassword)
	}
	return nil
}

// Add adds a user with password.
func (f *File) Add(name, password string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if _, ok := f.Users[name]; ok {
		return fmt.Errorf("user %q exists: naut users passwd changes the password", name)
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	h, err := HashPassword(password, Default)
	if err != nil {
		return err
	}
	f.Users[name] = &User{Password: h}
	return nil
}

func (f *File) user(name string) (*User, error) {
	u, ok := f.Users[name]
	if !ok {
		return nil, fmt.Errorf("no user %q", name)
	}
	return u, nil
}

// SetPassword changes a user's password.
func (f *File) SetPassword(name, password string) error {
	u, err := f.user(name)
	if err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	h, err := HashPassword(password, Default)
	if err != nil {
		return err
	}
	u.Password = h
	return nil
}

// SetDisabled disables or enables a user. A disabled user cannot sign in
// and none of the user's tokens is accepted.
func (f *File) SetDisabled(name string, disabled bool) error {
	u, err := f.user(name)
	if err != nil {
		return err
	}
	u.Disabled = disabled
	return nil
}

// Authenticate checks name's password. Every outcome takes one argon2id
// verification, so the time taken does not say whether the user exists.
func (f *File) Authenticate(name, password string) error {
	u, ok := f.Users[strings.ToLower(name)]
	if !ok || u.Disabled {
		verify(dummyHash(), password)
		if !ok {
			return ErrNoUser
		}
		return ErrDisabled
	}
	if !verify(u.Password, password) {
		return ErrWrongPassword
	}
	return nil
}

// Personal access tokens are "naut_pat_<id>_<secret>": a 16-hex-digit id
// to find the token by, and 32 random bytes, base64url.
const (
	TokenPrefix = "naut_pat_"
	idLen       = 16
)

func validTokenID(id string) bool {
	if len(id) != idLen {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// IsToken reports whether s has a personal access token's form.
func IsToken(s string) bool {
	id, _, ok := splitToken(s)
	return ok && validTokenID(id)
}

func splitToken(s string) (id, secret string, ok bool) {
	rest, ok := strings.CutPrefix(s, TokenPrefix)
	if !ok || len(rest) < idLen+2 || rest[idLen] != '_' {
		return "", "", false
	}
	return rest[:idLen], rest[idLen+1:], true
}

func tokenHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// NewToken issues a personal access token for name and returns it (shown
// once: only its hash is kept) and its id (to revoke it by).
func (f *File) NewToken(name string) (token, id string, err error) {
	u, err := f.user(name)
	if err != nil {
		return "", "", err
	}
	idb, sec := make([]byte, idLen/2), make([]byte, 32)
	if _, err := rand.Read(idb); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(sec); err != nil {
		return "", "", err
	}
	id, secret := hex.EncodeToString(idb), base64.RawURLEncoding.EncodeToString(sec)
	u.Tokens = append(u.Tokens, Token{ID: id, Hash: tokenHash(secret), Created: time.Now().UTC().Truncate(time.Second)})
	return TokenPrefix + id + "_" + secret, id, nil
}

// Revoke removes name's token id.
func (f *File) Revoke(name, id string) error {
	u, err := f.user(name)
	if err != nil {
		return err
	}
	for i, t := range u.Tokens {
		if t.ID == id {
			u.Tokens = append(u.Tokens[:i], u.Tokens[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("user %q has no token %s", name, id)
}

// TokenUser is the user a personal access token belongs to: false when it
// is no token of this file's, or its user is disabled. The secret is
// compared in constant time.
func (f *File) TokenUser(token string) (string, bool) {
	id, secret, ok := splitToken(token)
	if !ok || !validTokenID(id) {
		return "", false
	}
	want := tokenHash(secret)
	for name, u := range f.Users {
		for _, t := range u.Tokens {
			if t.ID != id {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) == 1 && !u.Disabled {
				return name, true
			}
			return "", false
		}
	}
	return "", false
}

// Names are the file's users, sorted.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Users))
	for n := range f.Users {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Digest is the SHA-256 of the file's credentials alone: each user's name,
// password hash, disabled flag and token ids and hashes, in a canonical
// order. Who changed the file and when are left out, so two controllers
// with the same credentials match however they got them (a redundant
// pair's handshake compares it).
func (f *File) Digest() [32]byte {
	type tok struct{ ID, Hash string }
	type usr struct {
		Name, Password string
		Disabled       bool
		Tokens         []tok
	}
	var all []usr
	for _, n := range f.Names() {
		u := f.Users[n]
		e := usr{Name: n, Password: u.Password, Disabled: u.Disabled}
		for _, t := range u.Tokens {
			e.Tokens = append(e.Tokens, tok{t.ID, t.Hash})
		}
		sort.Slice(e.Tokens, func(i, j int) bool { return e.Tokens[i].ID < e.Tokens[j].ID })
		all = append(all, e)
	}
	b, _ := json.Marshal(all)
	return sha256.Sum256(b)
}

// Digest is the credentials digest of the users file at path (File.Digest):
// all zeros when there is no file, an error when it cannot be read or
// parsed.
func Digest(path string) ([32]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, nil
	}
	if err != nil {
		return [32]byte{}, err
	}
	f, err := parse(b, path)
	if err != nil {
		return [32]byte{}, err
	}
	return f.Digest(), nil
}

// Change is one difference between two versions of the file, for the
// journal: never a hash or a secret.
type Change struct {
	User string // the user changed
	// Principal is the journal's target when it is not user:<User>: an
	// OIDC name's pin (oidc:<name>), or the principal a sign-out names.
	Principal string
	What      string // added, removed, password, disabled, enabled, token issued, token revoked; pinned, unpinned, pin changed; signed out
	Token     string // the token's id, for a token change
	Detail    string
	// Stopped marks a change made while the controller was not running,
	// found against its record of what it last saw (Store.CatchUp).
	Stopped bool
}

// Diff is what changed from old to new, sorted by user.
func Diff(old, new *File) []Change {
	if old == nil {
		old = New()
	}
	if new == nil {
		new = New()
	}
	var out []Change
	names := map[string]bool{}
	for n := range old.Users {
		names[n] = true
	}
	for n := range new.Users {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		o, nu := old.Users[n], new.Users[n]
		switch {
		case o == nil:
			out = append(out, Change{User: n, What: "added"})
			if nu.Disabled {
				out = append(out, Change{User: n, What: "disabled"})
			}
			for _, t := range nu.Tokens {
				out = append(out, Change{User: n, What: "token issued", Token: t.ID})
			}
			continue
		case nu == nil:
			out = append(out, Change{User: n, What: "removed"})
			continue
		}
		if o.Password != nu.Password {
			out = append(out, Change{User: n, What: "password"})
		}
		if o.Disabled != nu.Disabled {
			what := "enabled"
			if nu.Disabled {
				what = "disabled"
			}
			out = append(out, Change{User: n, What: what})
		}
		ot, nt := map[string]string{}, map[string]string{}
		for _, t := range o.Tokens {
			ot[t.ID] = t.Hash
		}
		for _, t := range nu.Tokens {
			nt[t.ID] = t.Hash
		}
		for _, t := range nu.Tokens {
			if h, ok := ot[t.ID]; !ok || h != t.Hash {
				out = append(out, Change{User: n, What: "token issued", Token: t.ID})
			}
		}
		for _, t := range o.Tokens {
			if h, ok := nt[t.ID]; !ok || h != t.Hash {
				out = append(out, Change{User: n, What: "token revoked", Token: t.ID})
			}
		}
	}
	return out
}
