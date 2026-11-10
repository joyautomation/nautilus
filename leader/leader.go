// Package leader elects exactly one control-owning PLC replica using a
// Kubernetes coordination.k8s.io/v1 Lease — the same primitive
// kube-controller-manager uses — via internal/k8sapi against the API server
// with the pod's service account. Pure stdlib.
//
// Outside a cluster (no service account mounted) the elector runs in
// standalone mode and is always the leader.
package leader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

type leaseSpec struct {
	HolderIdentity       string `json:"holderIdentity,omitempty"`
	LeaseDurationSeconds int    `json:"leaseDurationSeconds,omitempty"`
	AcquireTime          string `json:"acquireTime,omitempty"`
	RenewTime            string `json:"renewTime,omitempty"`
	LeaseTransitions     int    `json:"leaseTransitions,omitempty"`
}

type lease struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		ResourceVersion string `json:"resourceVersion,omitempty"`
	} `json:"metadata"`
	Spec leaseSpec `json:"spec"`
}

// Status is what the HMI shows about redundancy.
type Status struct {
	Mode        string `json:"mode"` // cluster | standalone
	Pod         string `json:"pod"`
	Leader      string `json:"leader"`
	LeaderAddr  string `json:"-"` // host:port for standby proxying; json-excluded so pod IPs never reach a browser
	IsLeader    bool   `json:"isLeader"`
	Transitions int    `json:"transitions"`
}

type Elector struct {
	mu       sync.RWMutex
	identity string // display name (pod name)
	holderID string // what we write into the lease: "name|host:port"
	name     string
	duration time.Duration
	client   *k8sapi.Client
	mode     string

	isLeader    bool
	holder      string // current lease holderIdentity, verbatim
	transitions int
	lastRenewOK time.Time

	// decided is closed once the elector has made its first decision:
	// at once for a standalone elector, after Run's first tick in a
	// cluster. See Decided.
	decided     chan struct{}
	decidedOnce sync.Once

	// health is asked before every renew and every acquisition (see
	// SetHealth); nil means always live. unhealthy is the last problem it
	// reported, "" while live: the transition is logged once each way.
	health    Health
	unhealthy string

	// term counts the leaderships this elector has held: it moves when it
	// creates or takes the lease, or renews one whose transitions or
	// acquire time differ from the last it held (termKey), and not when it
	// renews the same lease after a pause. See Term.
	term    uint64
	termKey string
}

// Term identifies this replica's current leadership: it changes whenever
// another replica may have led since this one last did, and stays the same
// when this replica closes its gate and opens it again on a lease nobody
// else held in between (scans that stalled, then completed). The runtime
// resumes on an unchanged term instead of taking over: no retained reload,
// no frame reset (runtime.Termed). A standalone elector has one term.
func (e *Elector) Term() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.term
}

// Health reports whether this replica's scans are live at now: nil when
// they are, else an error naming what is stuck. max is the longest one
// scan may be in progress before it counts as stuck: the elector passes
// half its lease. runtime.(*Runtime).Liveness is one.
type Health func(now time.Time, max time.Duration) error

// SetHealth makes the lease depend on live scans (#621): the elector asks
// h before it renews and before it acquires. A leader whose scans are not
// completing does not renew: it closes its own gate (IsLeader false) on its
// first tick more than half a lease after its last renew, its lease lapses
// a lease duration after that renew, and a standby takes over on its next
// tick. A replica whose scans are not completing does not acquire, so a
// broken replica cannot take over. A standalone elector, with no lease and
// no one to hand over to, closes its gate instead (IsLeader false) a lease
// duration after its last live check, and opens it again once scans are
// live. Call it before Run.
func (e *Elector) SetHealth(h Health) {
	e.mu.Lock()
	e.health = h
	e.mu.Unlock()
}

// live asks the health func and logs a change of answer once: loudly when
// scans stop completing, at info when they are live again.
func (e *Elector) live(now time.Time) bool {
	e.mu.RLock()
	h, was := e.health, e.unhealthy
	e.mu.RUnlock()
	if h == nil {
		return true
	}
	why, stopped := "", false
	if err := h(now, e.duration/2); err != nil {
		why, stopped = err.Error(), errors.Is(err, context.Canceled)
	}
	if why == was {
		return why == ""
	}
	e.mu.Lock()
	e.unhealthy = why
	lead := e.isLeader
	e.mu.Unlock()
	switch {
	case why == "":
		slog.Info("redundancy: scans are completing again; this replica renews and contends for the lease", "lease", e.name)
	case stopped:
		// An orderly stop: Run has returned. Not renewing keeps a teardown
		// that hangs from holding the lease; Release hands it over sooner.
		slog.Info("redundancy: the scan loops have stopped; this replica no longer renews the lease", "lease", e.name)
	case was != "":
		slog.Error("redundancy: scans are still not completing", "lease", e.name, "reason", why)
	case lead:
		slog.Error("redundancy: scans are not completing; this leader stops renewing the lease and hands over when it lapses", "lease", e.name, "lapsesIn", e.duration, "reason", why)
	default:
		slog.Error("redundancy: scans are not completing; this replica will not take the lease", "lease", e.name, "reason", why)
	}
	return why == ""
}

// Decided is closed once the elector has made its first decision, leader
// or standby. Until then IsLeader reports false in a cluster only because
// nothing has been asked yet; a caller that treats "the write gate was
// seen closed" as "this replica was a standby" (modbus and opcua-client
// drop their start-up baseline, #606) waits for it before starting them.
func (e *Elector) Decided() <-chan struct{} { return e.decided }

func (e *Elector) markDecided() { e.decidedOnce.Do(func() { close(e.decided) }) }

// New builds an elector for the named lease. identity should be the pod
// name; selfAddr (host:port) is advertised in the lease so standbys can
// proxy API traffic to whoever holds it.
//
// Outside a cluster — or with a service account nautilus can't read (RBAC
// misconfigured, token not yet mounted) — this degrades to standalone mode
// rather than failing startup: a single instance with no lease still needs
// to run and consider itself the leader.
func New(leaseName, identity, selfAddr string) (*Elector, error) {
	if !k8sapi.InCluster() {
		return newStandalone(leaseName, identity, selfAddr), nil
	}
	c, err := k8sapi.New()
	if err != nil {
		return newStandalone(leaseName, identity, selfAddr), nil
	}
	return newWithClient(c, leaseName, identity, selfAddr), nil
}

// resolveIdentity fills identity from the hostname when unset and derives
// the holderIdentity written into the lease. The pod's address rides inside
// holderIdentity as "identity|selfAddr" so a standby discovers the proxy
// target straight from the lease it's already reading, with no separate
// Endpoints lookup. Status splits it back apart with strings.Cut.
func resolveIdentity(identity, selfAddr string) (string, string) {
	if identity == "" {
		identity, _ = os.Hostname()
	}
	holderID := identity
	if selfAddr != "" {
		holderID = identity + "|" + selfAddr
	}
	return identity, holderID
}

func newStandalone(leaseName, identity, selfAddr string) *Elector {
	identity, holderID := resolveIdentity(identity, selfAddr)
	return &Elector{
		identity: identity,
		holderID: holderID,
		name:     leaseName,
		duration: 4 * time.Second,
		mode:     "standalone",
		isLeader: true,
		holder:   holderID,
		decided:  closedChan(),
		term:     1,
	}
}

// NewLocal builds a standalone elector for a project without
// `redundancy:` (#625): there is no lease and nothing to hand over to, but
// with SetHealth and Run it is a write gate that is open while the scans
// are live and closes a lease duration (lease) after the last check that
// found them live, so a driver's own loop (a Modbus rewrite: keep-alive)
// stops asserting outputs for a scan that is hung. It is open from New:
// a healthy project's drivers never see it closed.
func NewLocal(name string, lease time.Duration) *Elector {
	e := newStandalone(name, "", "")
	e.duration = lease
	return e
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// newWithClient builds a cluster-mode elector against c. Split out from New
// so a test can point it at an httptest server instead of a real API
// server; duration is a plain field a test can shorten to avoid real waits.
func newWithClient(c *k8sapi.Client, leaseName, identity, selfAddr string) *Elector {
	identity, holderID := resolveIdentity(identity, selfAddr)
	return &Elector{
		identity: identity,
		holderID: holderID,
		name:     leaseName,
		// A crashed/paused leader is detected after the lease expires, so
		// this bounds worst-case failover. 4s renews every 1s — snappy
		// without risking false failover on a brief GC/network hiccup. A
		// gracefully deleted leader (Release on SIGTERM) fails over faster.
		duration: 4 * time.Second,
		client:   c,
		mode:     "cluster",
		decided:  make(chan struct{}),
	}
}

// Run drives the acquire/renew loop until ctx is done.
func (e *Elector) Run(ctx context.Context) {
	if e.mode == "standalone" {
		e.runStandalone(ctx)
		return
	}
	// The first decision is made at once, not a tick in: until it is,
	// a replica that will lead would look like a standby (see Decided).
	// A tick that never reached the API decides nothing, so it is tried
	// again, every duration/16, for up to one lease duration: an API blip
	// at start must not turn a replica that will lead into a standby.
	for deadline := time.Now().Add(e.duration); !e.tick(time.Now()) && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			e.markDecided()
			return
		case <-time.After(e.duration / 16):
		}
	}
	e.markDecided()
	tick := time.NewTicker(e.duration / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.tick(time.Now())
		}
	}
}

// runStandalone is Run without a cluster. Without a health func there is
// nothing to do: a standalone elector is always the leader. With one, it
// ticks like a cluster elector and keeps a lease of its own: live scans
// renew it, and after a lease duration without them the gate closes, so a
// driver's own loop (a keep-alive, a device watchdog's feed) stops
// asserting outputs for a scan that is hung, and the device's fail-safe
// can act. Live scans open it again in the same term (Term), so the
// runtime resumes rather than taking over.
func (e *Elector) runStandalone(ctx context.Context) {
	e.mu.RLock()
	h := e.health
	e.mu.RUnlock()
	if h == nil {
		return
	}
	e.standaloneTick(time.Now())
	tick := time.NewTicker(e.duration / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.standaloneTick(time.Now())
		}
	}
}

// standaloneTick is a standalone elector's renew: leader while scans are
// live, stepping down a lease duration after they stopped.
func (e *Elector) standaloneTick(now time.Time) {
	if e.live(now) {
		e.setLeader(true, e.holderID, 0, now)
		return
	}
	e.maybeStepDown(now)
}

// tick takes now as a parameter (rather than reading time.Now() itself) so
// tests can drive the acquire/renew/steal/follow state machine with
// controlled clocks instead of real sleeps. It reports whether it reached
// the API (the Lease GET answered).
//
// Every step-down decision taken after an API call reads the time again
// (later), as the call may have taken seconds: a decision on the time the
// tick began would keep a leader that should already have stepped down.
func (e *Elector) tick(now time.Time) bool {
	start := time.Now()
	later := func() time.Time { return now.Add(time.Since(start)) }

	// Scans that are not completing neither renew nor acquire (#621). A
	// hung leader's step-down needs no API: it happens here, before the
	// GET, so a GET that fails or hangs cannot keep the gate open while a
	// standby takes the lease.
	live := e.live(now)
	if !live {
		e.stepDownAfter(now, e.duration/2)
	}

	l, code, err := e.get()
	if err != nil {
		e.maybeStepDown(later())
		if !live {
			e.stepDownAfter(later(), e.duration/2)
		}
		return false
	}

	switch {
	case code == http.StatusNotFound:
		// No lease yet — first replica up claims it.
		if !live {
			e.stepDownAfter(later(), e.duration/2)
			break
		}
		fresh := e.newLease(now, 1)
		if err := e.post(fresh); err == nil {
			e.lead(&fresh.Spec, now)
		}

	case l.Spec.HolderIdentity == e.holderID && !live:
		// We hold it but our scans are hung: no renew, so the lease lapses
		// one duration after the last renew and a standby takes over on
		// its next tick. We step down on our first tick more than half a
		// lease after the last renew, a tick or more before the lease can
		// lapse, not at the lapse as a partitioned leader must: this
		// replica can still see the lease, so it gives up its gate (a
		// driver's keep-alive, its own output flush) before any standby
		// can claim it, and two replicas never write at once. Live scans
		// before the lapse renew and lead again, in the same term.
		e.stepDownAfter(later(), e.duration/2)

	case l.Spec.HolderIdentity == e.holderID:
		// We hold it — renew. The duration is written again so a Lease
		// that something else wrote (by hand, GitOps, another tool) says
		// what this replica's own fence enforces from the first renew on
		// (#635).
		l.Spec.RenewTime = mtime(now)
		l.Spec.LeaseDurationSeconds = e.leaseSeconds()
		if err := e.put(l); err == nil {
			e.lead(&l.Spec, now)
		} else {
			e.maybeStepDown(later())
		}

	case e.expired(l, now) && live:
		// Holder went quiet past the lease duration — take over. l still
		// carries the resourceVersion from the GET above, so this put is a
		// compare-and-swap: if another standby's steal lands first, ours
		// comes back 409 and we fall through to staying standby.
		l.Spec.HolderIdentity = e.holderID
		l.Spec.AcquireTime = mtime(now)
		l.Spec.RenewTime = mtime(now)
		l.Spec.LeaseDurationSeconds = e.leaseSeconds()
		l.Spec.LeaseTransitions++
		if err := e.put(l); err == nil {
			e.lead(&l.Spec, now)
		}
		// 409 conflict: another standby won the race — stay standby.

	default:
		e.setLeader(false, l.Spec.HolderIdentity, l.Spec.LeaseTransitions, now)
	}
	return true
}

// expired reports whether the holder of l has gone quiet past its lease.
// The lease is judged by the longer of the stored duration and this
// replica's own: a Lease written short (or with none) by something else
// must not let a standby take it from a live holder whose fence still
// keeps its gate open (#635).
func (e *Elector) expired(l *lease, now time.Time) bool {
	rt, err := time.Parse(time.RFC3339Nano, l.Spec.RenewTime)
	if err != nil {
		return true
	}
	d := e.duration
	if s := int64(l.Spec.LeaseDurationSeconds); s > math.MaxInt64/int64(time.Second) {
		return false // a stored duration past ~292 years never lapses
	} else if stored := time.Duration(s) * time.Second; stored > d {
		d = stored
	}
	return now.After(rt.Add(d))
}

// leaseSeconds is this replica's lease duration as a Lease stores it, in
// whole seconds rounded up, so a replica reading it never judges the lease
// to lapse before this one's own fence has closed the gate.
func (e *Elector) leaseSeconds() int {
	return int((e.duration + time.Second - 1) / time.Second)
}

// maybeStepDown relinquishes leadership if we haven't successfully renewed
// within the lease duration — someone else may already own it. This is a
// purely time-based fence: a partitioned leader can't confirm a successor
// exists (its GETs are failing too), so it must stop claiming to lead once
// its own lease would have expired, rather than waiting for confirmation
// that will never arrive.
func (e *Elector) maybeStepDown(now time.Time) { e.stepDownAfter(now, e.duration) }

// stepDownAfter relinquishes leadership once the last renew is more than
// d old.
func (e *Elector) stepDownAfter(now time.Time, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.isLeader && now.Sub(e.lastRenewOK) > d {
		e.isLeader = false
	}
}

// lead records a successful create, renew or take-over of the lease s at
// now (the tick's start: a renew is dated no later than it was sent). The
// term moves unless s is the lease this elector last held, unchanged.
func (e *Elector) lead(s *leaseSpec, now time.Time) {
	key := fmt.Sprintf("%d|%s", s.LeaseTransitions, s.AcquireTime)
	e.mu.Lock()
	if key != e.termKey {
		e.term++
		e.termKey = key
	}
	e.mu.Unlock()
	e.setLeader(true, e.holderID, s.LeaseTransitions, now)
}

func (e *Elector) setLeader(lead bool, holder string, transitions int, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isLeader = lead
	e.holder = holder
	e.transitions = transitions
	if lead {
		e.lastRenewOK = now
	}
}

// Release relinquishes the lease if we hold it, by expiring its renew time
// so a standby takes over on its next tick instead of waiting the full
// lease duration. Called on graceful shutdown (SIGTERM) — a deleted or
// rolling-updated leader hands off in ~1s rather than ~4s.
//
// This does not delete the Lease: the pod's RBAC grants only get/create/
// update on leases, not delete, so re-fetching, confirming we still hold
// it, and writing an already-expired RenewTime is the only way to give it
// up early within that grant.
func (e *Elector) Release() {
	if e.mode != "cluster" {
		return
	}
	e.mu.RLock()
	held := e.isLeader
	e.mu.RUnlock()
	if !held {
		return
	}
	l, code, err := e.get()
	if err != nil || code != http.StatusOK || l.Spec.HolderIdentity != e.holderID {
		return
	}
	l.Spec.RenewTime = mtime(time.Unix(0, 0)) // immediately expired
	_ = e.put(l)
	e.mu.Lock()
	e.isLeader = false
	e.mu.Unlock()
}

// IsLeader reports whether this replica leads. In a cluster it is also
// fenced by time, read now rather than on the elector's next tick: a
// leader whose last renew is older than the lease (half the lease while
// its scans are not completing) does not lead, however late its own tick
// or its API call is. A starved or blocked elector goroutine cannot keep
// the gate open while a standby takes the lease.
func (e *Elector) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.leadingLocked()
}

func (e *Elector) leadingLocked() bool {
	if !e.isLeader || e.mode != "cluster" {
		return e.isLeader
	}
	fence := e.duration
	if e.unhealthy != "" {
		fence = e.duration / 2
	}
	return time.Since(e.lastRenewOK) <= fence
}

func (e *Elector) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	name, addr, _ := strings.Cut(e.holder, "|")
	return Status{
		Mode: e.mode, Pod: e.identity, Leader: name, LeaderAddr: addr,
		IsLeader: e.leadingLocked(), Transitions: e.transitions,
	}
}

// ---- Lease API plumbing ----

func (e *Elector) leasePath() string {
	return "/apis/coordination.k8s.io/v1/namespaces/" + e.client.Namespace + "/leases/" + e.name
}

func (e *Elector) newLease(now time.Time, transitions int) *lease {
	l := &lease{APIVersion: "coordination.k8s.io/v1", Kind: "Lease"}
	l.Metadata.Name = e.name
	l.Metadata.Namespace = e.client.Namespace
	l.Spec = leaseSpec{
		HolderIdentity:       e.holderID,
		LeaseDurationSeconds: e.leaseSeconds(),
		AcquireTime:          mtime(now),
		RenewTime:            mtime(now),
		LeaseTransitions:     transitions,
	}
	return l
}

func (e *Elector) get() (*lease, int, error) {
	resp, err := e.client.Do(http.MethodGet, e.leasePath(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("get lease: %s", resp.Status)
	}
	var l lease
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return nil, resp.StatusCode, err
	}
	return &l, resp.StatusCode, nil
}

func (e *Elector) post(l *lease) error {
	collection := "/apis/coordination.k8s.io/v1/namespaces/" + e.client.Namespace + "/leases"
	return e.send(http.MethodPost, collection, l, http.StatusCreated)
}

// put updates the lease; the resourceVersion carried in l (round-tripped
// from the preceding get) makes this a compare-and-swap — a concurrent
// takeover that lands first bumps the resourceVersion server-side, and our
// put with the stale one back gets 409 and loses. This CAS is the entire
// correctness mechanism behind "exactly one leader": keep the get -> mutate
// -> put flow intact wherever the holder changes.
func (e *Elector) put(l *lease) error {
	return e.send(http.MethodPut, e.leasePath(), l, http.StatusOK)
}

func (e *Elector) send(method, path string, l *lease, want int) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(method, path, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s lease: %s: %s", method, resp.Status, body)
	}
	return nil
}

func mtime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}
