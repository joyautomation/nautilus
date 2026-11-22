package runtime

import (
	"sync"

	"github.com/joyautomation/nautilus/retain"
)

// PushKeeper is the Web Push subscriptions (#722), which the runtime only
// carries: retainState asks it what to keep, and a load (a start, or a
// takeover) hands it back what the store holds. The server implements it.
type PushKeeper interface {
	// RetainedPush is what to keep, nil when there are no subscriptions.
	RetainedPush() *retain.PushState
	// RestorePush is the store's subscriptions after a load (nil: none).
	RestorePush(st *retain.PushState)
}

type pushKeep struct {
	mu sync.Mutex
	k  PushKeeper
	// loaded is the subscriptions of the last store load, for a keeper
	// registered after it: naut run loads the store (LoadRetained) before
	// it builds the server.
	loaded *retain.PushState
}

// SetPush registers the subscriptions' keeper. Call it before Run (and
// before LoadRetained), as SetAlarms.
func (r *Runtime) SetPush(k PushKeeper) {
	r.pushk.mu.Lock()
	r.pushk.k = k
	loaded := r.pushk.loaded
	r.pushk.mu.Unlock()
	if loaded != nil && k != nil {
		k.RestorePush(loaded)
	}
}

// pushLoaded is loadRetained's hand-over: it tells the keeper, and keeps
// what it was told for one registered later.
func (r *Runtime) pushLoaded(st *retain.PushState) {
	r.pushk.mu.Lock()
	r.pushk.loaded = st
	k := r.pushk.k
	r.pushk.mu.Unlock()
	if k != nil {
		k.RestorePush(st)
	}
}

func (r *Runtime) pushKeeper() PushKeeper {
	r.pushk.mu.Lock()
	defer r.pushk.mu.Unlock()
	return r.pushk.k
}

// pushHeld is the subscriptions of the last load, for a save made while no
// keeper is registered: written back unchanged.
func (r *Runtime) pushHeld() *retain.PushState {
	r.pushk.mu.Lock()
	defer r.pushk.mu.Unlock()
	return r.pushk.loaded
}
