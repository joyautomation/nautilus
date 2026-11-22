package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxPerPrincipal is how many devices one person may be paged on; a newer
// subscription ends the oldest.
const MaxPerPrincipal = 8

// Subscription is what a browser's PushManager.subscribe returns: the
// endpoint is where a message is POSTed, p256dh and auth are the keys its
// payload is encrypted to. All three are secrets (the endpoint alone lets
// anyone with the VAPID key page that browser): they are held sealed and
// never logged, journalled or returned by any API.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Row is one subscription as it is kept (and retained): everything about
// it that is not secret in the clear, the secrets sealed.
type Row struct {
	// ID names the subscription in the API: a hash of the endpoint.
	ID        string    `json:"id"`
	Principal string    `json:"principal"`
	Host      string    `json:"host"` // the push service's host, for the person's list
	At        time.Time `json:"at"`
	// Groups are an OIDC principal's group ids when it subscribed: what its
	// grants were read from, for the standing check and the read gate.
	Groups []string `json:"groups,omitempty"`
	// Key fingerprints the VAPID key the row is sealed under: another
	// key's row is kept, not sent, and counted.
	Key string `json:"key"`
	// Cred is what the principal's sign-in was proved with when it
	// subscribed (a password's fingerprint, or an OIDC account's): the
	// subscription holds only while the principal still proves the same.
	Cred   string `json:"cred,omitempty"`
	Sealed string `json:"sealed"`
}

var (
	// ErrSealed says a row cannot be opened: it was sealed under another
	// push key. It is kept, not sent, never dropped for it.
	ErrSealed = errors.New("webpush: the subscription is sealed under another push key")
	// ErrEndpoint refuses an endpoint a controller may not be pointed at.
	ErrEndpoint = errors.New("webpush: not an acceptable push endpoint")
)

// Policy is where a controller may send: an https endpoint on a host the
// allow list names, or, when the list is empty, on any public address.
type Policy struct {
	// AllowHosts are host names ("push.example.net", "*.example.net") that
	// may be sent to; naming one also allows it to resolve to a private
	// address (a site's own push relay). Empty: any public host.
	AllowHosts []string
}

func (p Policy) allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range p.AllowHosts {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == host || strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:]) && len(host) > len(a)-1 {
			return true
		}
	}
	return false
}

// CheckEndpoint is the syntax and policy check made when a subscription
// arrives: https, a host, no credentials, bounded. What the host resolves
// to is checked again at dial time (Sender), never trusted from here.
func (p Policy) CheckEndpoint(endpoint string) (host string, err error) {
	if len(endpoint) > 2048 {
		return "", ErrEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", ErrEndpoint
	}
	host = strings.ToLower(u.Hostname())
	if len(p.AllowHosts) > 0 && !p.allows(host) {
		return "", ErrEndpoint
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) && !p.allows(host) {
		return "", ErrEndpoint
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		if !p.allows(host) {
			return "", ErrEndpoint
		}
	}
	return host, nil
}

// Registry is the set of subscriptions, sealed, in memory. Persisting it
// is the caller's: Rows is what to keep, Load what a store gave back.
type Registry struct {
	aead cipher.AEAD
	fp   string

	mu   sync.Mutex
	rows []Row
}

// NewRegistry makes an empty registry sealing under key.
func NewRegistry(key *Key) (*Registry, error) {
	sk, err := hkdf.Key(sha256.New, []byte(key.Private()), nil, "nautilus webpush seal v1", 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(sk)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Registry{aead: g, fp: key.Fingerprint()}, nil
}

// KeyFingerprint names the key the registry seals under.
func (g *Registry) KeyFingerprint() string { return g.fp }

// EndpointID is the ID a subscription to endpoint has in the API.
func EndpointID(endpoint string) string { return rowID(endpoint) }

func rowID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}

func (g *Registry) seal(id, principal string, s Subscription) (string, error) {
	pt, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	n := make([]byte, g.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	return b64.EncodeToString(g.aead.Seal(n, n, pt, []byte(id+"\x00"+principal))), nil
}

// Open unseals a row. A row sealed under another key, or altered, or moved
// to another principal, is ErrSealed.
func (g *Registry) Open(r Row) (Subscription, error) {
	if r.Key != g.fp {
		return Subscription{}, ErrSealed
	}
	raw, err := b64.DecodeString(r.Sealed)
	if err != nil || len(raw) < g.aead.NonceSize() {
		return Subscription{}, ErrSealed
	}
	n, ct := raw[:g.aead.NonceSize()], raw[g.aead.NonceSize():]
	pt, err := g.aead.Open(nil, n, ct, []byte(r.ID+"\x00"+r.Principal))
	if err != nil {
		return Subscription{}, ErrSealed
	}
	var s Subscription
	if err := json.Unmarshal(pt, &s); err != nil {
		return Subscription{}, ErrSealed
	}
	return s, nil
}

// Change says what Add did to other people's rows, for the journal.
type Change struct {
	Taken   []Row // another principal's row for the same endpoint (newest wins)
	Evicted []Row // this principal's oldest, over the cap
	Replace bool  // this principal's own row for the endpoint was replaced
}

// Add keeps s for principal, proved with cred. One row per endpoint: it is
// one browser profile, so the newest signed-in principal takes it. Over
// MaxPerPrincipal the principal's oldest ends. The caller has checked the
// endpoint (Policy.CheckEndpoint) and the keys' shape (Validate).
func (g *Registry) Add(principal string, groups []string, cred, host string, s Subscription, now time.Time) (Row, Change, error) {
	id := rowID(s.Endpoint)
	sealed, err := g.seal(id, principal, s)
	if err != nil {
		return Row{}, Change{}, err
	}
	row := Row{ID: id, Principal: principal, Groups: groups, Key: g.fp, Host: host, At: now.UTC().Truncate(time.Second), Cred: cred, Sealed: sealed}
	g.mu.Lock()
	defer g.mu.Unlock()
	var ch Change
	keep := g.rows[:0:0]
	for _, r := range g.rows {
		switch {
		case r.ID == id && r.Principal == principal:
			ch.Replace = true
		case r.ID == id:
			ch.Taken = append(ch.Taken, r)
		default:
			keep = append(keep, r)
		}
	}
	keep = append(keep, row)
	var mine []int
	for i, r := range keep {
		if r.Principal == principal {
			mine = append(mine, i)
		}
	}
	for len(mine) > MaxPerPrincipal {
		oldest := mine[0]
		for _, i := range mine {
			if keep[i].At.Before(keep[oldest].At) {
				oldest = i
			}
		}
		ch.Evicted = append(ch.Evicted, keep[oldest])
		keep = append(keep[:oldest], keep[oldest+1:]...)
		mine = mine[:0]
		for i, r := range keep {
			if r.Principal == principal {
				mine = append(mine, i)
			}
		}
	}
	g.rows = keep
	return row, ch, nil
}

// Validate checks the shape of a subscription's keys (not the endpoint).
func Validate(s Subscription) error {
	pk, err := b64.DecodeString(strings.TrimRight(s.P256dh, "="))
	if err != nil || len(pk) != 65 || pk[0] != 4 {
		return errors.New("keys.p256dh is not a 65-octet uncompressed P-256 point in base64url")
	}
	au, err := b64.DecodeString(strings.TrimRight(s.Auth, "="))
	if err != nil || len(au) != 16 {
		return errors.New("keys.auth is not 16 octets in base64url")
	}
	return nil
}

// Remove ends principal's subscription id; false when there is none.
func (g *Registry) Remove(principal, id string) bool {
	return g.removeWhere(func(r Row) bool { return r.Principal == principal && r.ID == id }) > 0
}

// RemoveWhere ends every row f says, and returns them.
func (g *Registry) RemoveWhere(f func(Row) bool) []Row {
	g.mu.Lock()
	defer g.mu.Unlock()
	keep, gone := g.rows[:0:0], []Row(nil)
	for _, r := range g.rows {
		if f(r) {
			gone = append(gone, r)
		} else {
			keep = append(keep, r)
		}
	}
	g.rows = keep
	return gone
}

func (g *Registry) removeWhere(f func(Row) bool) int { return len(g.RemoveWhere(f)) }

// Of lists principal's rows, newest first.
func (g *Registry) Of(principal string) []Row {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Row
	for _, r := range g.rows {
		if r.Principal == principal {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// Rows is every row, for the store.
func (g *Registry) Rows() []Row {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Row(nil), g.rows...)
}

// Load replaces the rows with what a store holds.
func (g *Registry) Load(rows []Row) {
	g.mu.Lock()
	g.rows = append([]Row(nil), rows...)
	g.mu.Unlock()
}

// Sealed counts the rows this registry cannot open (sealed under another
// key): kept, not sent.
func (g *Registry) Sealed() int {
	n := 0
	for _, r := range g.Rows() {
		if _, err := g.Open(r); err != nil {
			n++
		}
	}
	return n
}
