// addrmap.go is the opt-in address map: by default every address of every
// table reads as zero (the friendliest shape for driver tests), but a
// virtual device must answer like the hardware it stands in for, which
// refuses an address it does not implement with exception 02 and a write
// to a read-only register the same way. Define switches a unit to that
// behaviour; a unit nobody calls Define on is unchanged.
package slave

// Table names, the same spellings the modbus manifest uses.
const (
	TableHolding  = "holding"
	TableInput    = "input"
	TableCoil     = "coil"
	TableDiscrete = "discrete"
)

// NewUnit returns a free-standing store, for a caller that reads and
// writes it in process (a virtual device stepped in virtual time) and may
// later serve it on a listener with SetUnit.
func NewUnit() *Unit { return newUnit() }

// SetUnit serves u as unit id, replacing any unit already there: several
// virtual devices behind one gateway address share a listener, each
// bringing its own store.
func (s *Server) SetUnit(id uint8, u *Unit) {
	s.mu.Lock()
	s.units[id] = u
	s.mu.Unlock()
}

// tableFC maps a function code to the table it addresses and whether it
// writes.
func tableFC(fc byte) (table string, write bool, ok bool) {
	switch fc {
	case 0x01:
		return TableCoil, false, true
	case 0x02:
		return TableDiscrete, false, true
	case 0x03:
		return TableHolding, false, true
	case 0x04:
		return TableInput, false, true
	case 0x05, 0x0F:
		return TableCoil, true, true
	case 0x06, 0x10:
		return TableHolding, true, true
	}
	return "", false, false
}

// Define declares count addresses of one table, starting at addr, as
// implemented; writable says whether a client may write them (only the
// holding and coil tables can be written at all). After the first Define
// the unit is strict: a request touching an address no Define covers, or
// writing one declared read-only, answers exception 02 (illegal data
// address), which is what a real device answers.
func (u *Unit) Define(table string, addr, count uint16, writable bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.defined == nil {
		u.defined = map[string]map[uint16]bool{}
	}
	m := u.defined[table]
	if m == nil {
		m = map[uint16]bool{}
		u.defined[table] = m
	}
	for i := range uint32(count) {
		a := uint32(addr) + i
		if a > 0xFFFF {
			break
		}
		// A register declared both ways (two points sharing it) is
		// writable if either says so.
		m[uint16(a)] = m[uint16(a)] || writable
	}
}

// refused reports whether a strict unit refuses this request's range: an
// address outside the map, or a write to one declared read-only. A unit
// with no map refuses nothing.
func (u *Unit) refused(fc byte, data []byte) bool {
	table, write, ok := tableFC(fc)
	if !ok || len(data) < 4 {
		return false
	}
	addr := uint32(data[0])<<8 | uint32(data[1])
	count := uint32(1)
	if fc != 0x05 && fc != 0x06 {
		count = uint32(data[2])<<8 | uint32(data[3])
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.defined == nil {
		return false
	}
	m := u.defined[table]
	for i := range count {
		w, ok := m[uint16(addr+i)]
		if !ok || (write && !w) {
			return true
		}
	}
	return false
}
