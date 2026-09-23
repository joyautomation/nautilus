// store.go is the slave's data model: one Unit per unit-id, four tables
// each, thread-safe so tests and a future `naut modbus serve` drive
// values from other goroutines while the server answers requests.
package slave

import "sync"

// Unit is one Modbus unit's register and coil space. Every address is
// implemented and reads as zero until set — the friendliest shape for tests;
// error paths are exercised with InjectException instead of holes in the
// map.
type Unit struct {
	mu       sync.Mutex
	holding  map[uint16]uint16
	input    map[uint16]uint16
	coils    map[uint16]bool
	discrete map[uint16]bool
	excepts  map[byte]byte // fc -> injected exception code
}

func newUnit() *Unit {
	return &Unit{
		holding:  map[uint16]uint16{},
		input:    map[uint16]uint16{},
		coils:    map[uint16]bool{},
		discrete: map[uint16]bool{},
		excepts:  map[byte]byte{},
	}
}

// SetHolding sets one holding register (what FC 3 reads and FC 6/16 write).
func (u *Unit) SetHolding(addr, v uint16) {
	u.mu.Lock()
	u.holding[addr] = v
	u.mu.Unlock()
}

// Holding reads one holding register.
func (u *Unit) Holding(addr uint16) uint16 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.holding[addr]
}

// SetInput sets one input register (what FC 4 reads).
func (u *Unit) SetInput(addr, v uint16) {
	u.mu.Lock()
	u.input[addr] = v
	u.mu.Unlock()
}

// Input reads one input register.
func (u *Unit) Input(addr uint16) uint16 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.input[addr]
}

// SetCoil sets one coil (what FC 1 reads and FC 5/15 write).
func (u *Unit) SetCoil(addr uint16, v bool) {
	u.mu.Lock()
	u.coils[addr] = v
	u.mu.Unlock()
}

// Coil reads one coil.
func (u *Unit) Coil(addr uint16) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.coils[addr]
}

// SetDiscrete sets one discrete input (what FC 2 reads).
func (u *Unit) SetDiscrete(addr uint16, v bool) {
	u.mu.Lock()
	u.discrete[addr] = v
	u.mu.Unlock()
}

// Discrete reads one discrete input.
func (u *Unit) Discrete(addr uint16) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.discrete[addr]
}

// InjectException makes every request with this function code answer with
// the given Modbus exception code until cleared with code 0 — how tests
// stand in for an FTIR's illegal-address collision or a busy gateway.
func (u *Unit) InjectException(fc, code byte) {
	u.mu.Lock()
	if code == 0 {
		delete(u.excepts, fc)
	} else {
		u.excepts[fc] = code
	}
	u.mu.Unlock()
}

// injected reports the exception code configured for fc, if any.
func (u *Unit) injected(fc byte) (byte, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	code, ok := u.excepts[fc]
	return code, ok
}
