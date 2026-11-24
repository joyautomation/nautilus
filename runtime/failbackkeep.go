package runtime

import (
	"sync"

	"github.com/joyautomation/nautilus/retain"
)

// FailbackKeeper is a redundant pair's failback state (#710 d2), which the
// runtime only carries: retainState asks it what to keep, and a load (a
// start, or a takeover) hands it what the store or the image held, and
// whether that could be read at all (known false: an unreadable store with
// no image to carry it). cmd/naut's pair implements it.
type FailbackKeeper interface {
	// RetainedFailback is what to keep (nil: nothing set).
	RetainedFailback() *retain.FailbackState
	// RestoreFailback is the loaded state; known is false when the store
	// could not be read and nothing else carried it.
	RestoreFailback(st *retain.FailbackState, known bool)
}

type failbackKeep struct {
	mu sync.Mutex
	k  FailbackKeeper
	// loaded and known are the last load's, for a keeper registered
	// after it (naut run loads the store before it builds the pair's
	// parts that read it).
	loaded *retain.FailbackState
	known  bool
	did    bool // a load has happened
}

// SetFailback registers the failback state's keeper. Call it before Run.
func (r *Runtime) SetFailback(k FailbackKeeper) {
	r.failk.mu.Lock()
	r.failk.k = k
	loaded, known, did := r.failk.loaded, r.failk.known, r.failk.did
	r.failk.mu.Unlock()
	if did && k != nil {
		k.RestoreFailback(loaded, known)
	}
}

// failbackLoaded is a load's hand-over: it tells the keeper, and keeps
// what it was told for one registered later.
func (r *Runtime) failbackLoaded(st *retain.FailbackState, known bool) {
	r.failk.mu.Lock()
	r.failk.loaded, r.failk.known, r.failk.did = st, known, true
	k := r.failk.k
	r.failk.mu.Unlock()
	if k != nil {
		k.RestoreFailback(st, known)
	}
}

// failbackHeld is what to save: the keeper's state, else the last load's
// written back unchanged.
func (r *Runtime) failbackHeld() *retain.FailbackState {
	r.failk.mu.Lock()
	k, loaded := r.failk.k, r.failk.loaded
	r.failk.mu.Unlock()
	if k != nil {
		return k.RetainedFailback()
	}
	return loaded
}
