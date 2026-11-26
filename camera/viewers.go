//go:build !js

package camera

import (
	"errors"
	"fmt"
	"sync"
)

// Limiter bounds who may watch live: a camera's own cap, and the controller's
// across every camera.
type Limiter struct {
	mu     sync.Mutex
	global int
	per    map[string]int
	who    map[string]int
	max    int // global cap
}

// MaxPerPerson is how many live streams one person (or token) may hold at
// once, so one reader cannot take every place the controller has.
const MaxPerPerson = 2

// NewLimiter makes a limiter with a global cap (MaxLive when 0).
func NewLimiter(global int) *Limiter {
	if global <= 0 {
		global = MaxLive
	}
	return &Limiter{per: map[string]int{}, who: map[string]int{}, max: global}
}

// ErrBusy says a cap is reached.
var ErrBusy = errors.New("camera busy")

// Acquire takes one live-viewer place on c, and returns what gives it back. It
// refuses (ErrBusy, with the reason) when the camera's viewers or the
// controller's live streams are all taken.
func (l *Limiter) Acquire(c Camera, who string) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.who[who] >= MaxPerPerson {
		return nil, fmt.Errorf("%w: you are already watching %d live streams", ErrBusy, MaxPerPerson)
	}
	if l.per[c.ID] >= c.Viewers() {
		return nil, fmt.Errorf("%w: %d people are already watching %s live (max-viewers)", ErrBusy, l.per[c.ID], c.ID)
	}
	if l.global >= l.max {
		return nil, fmt.Errorf("%w: this controller already relays %d live streams", ErrBusy, l.max)
	}
	l.per[c.ID]++
	l.who[who]++
	l.global++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.per[c.ID]--
			l.who[who]--
			l.global--
			l.mu.Unlock()
		})
	}, nil
}

// Watching is how many are watching c live now.
func (l *Limiter) Watching(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.per[id]
}
