package runtime

import (
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/retain"
)

type fakeFailbackKeeper struct {
	mu    sync.Mutex
	st    *retain.FailbackState
	known bool
	loads int
}

func (k *fakeFailbackKeeper) RetainedFailback() *retain.FailbackState {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.st
}

func (k *fakeFailbackKeeper) RestoreFailback(st *retain.FailbackState, known bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.st, k.known, k.loads = st, known, k.loads+1
}

// A load hands the keeper the store's failback state, known, and a save
// writes the keeper's back; a keeper registered after the load gets it
// too. An unreadable store hands it an unknown state, which the pair reads
// as suspended (#710 d2): failback_unknown_as_known turns it red.
func TestAnUnreadableStoreHandsAnUnknownFailbackState(t *testing.T) {
	store := &fakeStore{state: retain.State{Failback: &retain.FailbackState{Suspended: true, By: "user:op"}}}
	rt := newRetained(t, store, &flag{lead: true})
	rt.Scan() // the takeover loads the store
	k := &fakeFailbackKeeper{}
	rt.SetFailback(k) // after the load, as naut run may
	if k.loads != 1 || !k.known || k.st == nil || k.st.By != "user:op" {
		t.Fatalf("keeper after a load: %+v", k)
	}
	k.mu.Lock()
	k.st = &retain.FailbackState{Count: 2}
	k.mu.Unlock()
	rt.saveRetained(nil)
	store.mu.Lock()
	saved := store.state.Failback
	store.mu.Unlock()
	if saved == nil || saved.Count != 2 || saved.Suspended {
		t.Fatalf("saved failback %+v, want the keeper's", saved)
	}

	store.fail = true
	_ = rt.loadRetained()
	if k.loads != 2 || k.known {
		t.Fatalf("keeper after an unreadable store: loads %d known %v, want an unknown state", k.loads, k.known)
	}
}
