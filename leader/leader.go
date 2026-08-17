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
	"fmt"
	"io"
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
}

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
	}
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
	}
}

// Run drives the acquire/renew loop until ctx is done.
func (e *Elector) Run(ctx context.Context) {
	if e.mode == "standalone" {
		return
	}
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

// tick takes now as a parameter (rather than reading time.Now() itself) so
// tests can drive the acquire/renew/steal/follow state machine with
// controlled clocks instead of real sleeps.
func (e *Elector) tick(now time.Time) {
	l, code, err := e.get()
	if err != nil {
		e.maybeStepDown(now)
		return
	}

	switch {
	case code == http.StatusNotFound:
		// No lease yet — first replica up claims it.
		fresh := e.newLease(now, 1)
		if err := e.post(fresh); err == nil {
			e.setLeader(true, e.holderID, 1, now)
		}

	case l.Spec.HolderIdentity == e.holderID:
		// We hold it — renew.
		l.Spec.RenewTime = mtime(now)
		if err := e.put(l); err == nil {
			e.setLeader(true, e.holderID, l.Spec.LeaseTransitions, now)
		} else {
			e.maybeStepDown(now)
		}

	case e.expired(l, now):
		// Holder went quiet past the lease duration — take over. l still
		// carries the resourceVersion from the GET above, so this put is a
		// compare-and-swap: if another standby's steal lands first, ours
		// comes back 409 and we fall through to staying standby.
		l.Spec.HolderIdentity = e.holderID
		l.Spec.AcquireTime = mtime(now)
		l.Spec.RenewTime = mtime(now)
		l.Spec.LeaseTransitions++
		if err := e.put(l); err == nil {
			e.setLeader(true, e.holderID, l.Spec.LeaseTransitions, now)
		}
		// 409 conflict: another standby won the race — stay standby.

	default:
		e.setLeader(false, l.Spec.HolderIdentity, l.Spec.LeaseTransitions, now)
	}
}

func (e *Elector) expired(l *lease, now time.Time) bool {
	rt, err := time.Parse(time.RFC3339Nano, l.Spec.RenewTime)
	if err != nil {
		return true
	}
	d := time.Duration(l.Spec.LeaseDurationSeconds) * time.Second
	return now.After(rt.Add(d))
}

// maybeStepDown relinquishes leadership if we haven't successfully renewed
// within the lease duration — someone else may already own it. This is a
// purely time-based fence: a partitioned leader can't confirm a successor
// exists (its GETs are failing too), so it must stop claiming to lead once
// its own lease would have expired, rather than waiting for confirmation
// that will never arrive.
func (e *Elector) maybeStepDown(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.isLeader && now.Sub(e.lastRenewOK) > e.duration {
		e.isLeader = false
	}
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

func (e *Elector) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isLeader
}

func (e *Elector) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	name, addr, _ := strings.Cut(e.holder, "|")
	return Status{
		Mode: e.mode, Pod: e.identity, Leader: name, LeaderAddr: addr,
		IsLeader: e.isLeader, Transitions: e.transitions,
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
		LeaseDurationSeconds: int(e.duration.Seconds()),
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
