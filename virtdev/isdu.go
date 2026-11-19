package virtdev

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/iolink/iodd"
	uaserver "github.com/joyautomation/nautilus/opcua/server"
)

// A virtual IO-Link master answers ISDU for each port that has a device on
// it (a ports: entry with an IODD): ReadISDU and WriteISDU, as methods of
// the port device's object where the profile's isdu: block puts them, with
// the arguments and answers the block's mode and data form describe. The
// device's parameters are its IODD's variables: they start at the IODD's
// defaults (zeros where it has none), a write is checked against the IODD
// (the access rights, the length, the range) as a device would, and a read
// answers what was last written.
//
// Refusals carry the IO-Link ISDU error codes a device answers with. They
// are what a simulator says, not a measurement of any device: the values
// are from memory of the interface specification and unverified.
const (
	errIndexNotAvailable    = 0x8011
	errSubindexNotAvailable = 0x8012
	errAccessDenied         = 0x8023
	errValueOutOfRange      = 0x8030
	errLengthOverrun        = 0x8033
	errLengthUnderrun       = 0x8034
)

// isduStatusDevice is the Status a master answers for a refusal by the
// device: OPC 30120's -3 ("device error", the ErrorType says which); other
// masters' own code is unknown here, so 1.
func isduStatusDevice(mode string) int64 {
	if mode == devprofile.ISDUOPC30120 {
		return -3
	}
	return 1
}

// isduNodes are the Object and Method nodes of a master instance's ports'
// ISDU, added to the profile's tree. ensure places an Object by path.
func (s *Set) isduNodes(d Instance, p *devprofile.Profile, subst func(string) string, ensure func(string) (string, error)) ([]uaserver.MappedNode, error) {
	if len(d.Ports) == 0 {
		return nil, nil
	}
	if p.IOLink == nil || !p.IOLink.ISDU.Offers() {
		return nil, fmt.Errorf("device %s: ports: profile %s has no isdu: for the master to answer with", d.Name, p.ID)
	}
	spec := p.IOLink.ISDU
	nums := make([]int, 0, len(d.Ports))
	for n := range d.Ports {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []uaserver.MappedNode
	for _, n := range nums {
		if n < 1 || n > p.IOLink.Ports {
			return nil, fmt.Errorf("device %s: ports: profile %s has ports 1-%d, not %d", d.Name, p.ID, p.IOLink.Ports, n)
		}
		if !devprofile.IsFileName(d.Ports[n].IODD) {
			return nil, fmt.Errorf("device %s: port %d: iodd %q must be a file name in iodd/", d.Name, n, d.Ports[n].IODD)
		}
		raw, err := os.ReadFile(filepath.Join(s.Dir, "iodd", d.Ports[n].IODD))
		if err != nil {
			return nil, fmt.Errorf("device %s: port %d: %w", d.Name, n, err)
		}
		dev, err := iodd.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("device %s: port %d: iodd %s: %w", d.Name, n, d.Ports[n].IODD, err)
		}
		expand := func(tmpl string) string { return strings.ReplaceAll(subst(tmpl), "{n}", strconv.Itoa(n)) }
		objID := expand(spec.Object)
		_, path, ok := strings.Cut(objID, ";s=")
		if !ok {
			return nil, fmt.Errorf("device %s: isdu object %q is not a string node id", d.Name, objID)
		}
		placed, err := ensure(path)
		if err != nil {
			return nil, err
		}
		if placed != objID {
			return nil, fmt.Errorf("device %s: isdu object %s is not where the profile's tree puts it (%s)", d.Name, objID, placed)
		}
		store := &isduStore{iodd: dev, mode: spec.Mode, data: spec.Data, max: spec.Octets(), raw: map[uint32][]byte{}}
		out = append(out, uaserver.MappedNode{NodeID: expand(spec.Read), Parent: objID, Name: "ReadISDU", Method: store.read})
		if spec.Write != "" {
			out = append(out, uaserver.MappedNode{NodeID: expand(spec.Write), Parent: objID, Name: "WriteISDU", Method: store.write, Writes: true})
		}
	}
	return out, nil
}

// isduStore is one port device's parameters.
type isduStore struct {
	iodd *iodd.IODD
	mode string
	data string
	max  int
	mu   sync.Mutex
	raw  map[uint32][]byte // index<<8|subindex -> the octets last written
}

func key(idx, sub int) uint32 { return uint32(idx)<<8 | uint32(sub) }

// answer is an ISDU answer: a read's value (nil for a write), the IO-Link
// ErrorType and the master's Status.
func (st *isduStore) answer(read bool, value []byte, errType uint16, status int64) []*ua.Variant {
	var out []*ua.Variant
	if read {
		var v *ua.Variant
		if st.data == devprofile.ISDUDataHex {
			v, _ = ua.NewVariant("0x" + hex.EncodeToString(value))
		} else {
			v, _ = ua.NewVariant(ua.ByteArray(value))
		}
		out = append(out, v)
	}
	e, _ := ua.NewVariant(errType)
	var s *ua.Variant
	if st.mode == devprofile.ISDUOPC30120 {
		s, _ = ua.NewVariant(int32(status))
	} else {
		s, _ = ua.NewVariant(uint32(status))
	}
	return append(out, e, s)
}

func (st *isduStore) refuse(read bool, errType uint16) ([]*ua.Variant, ua.StatusCode) {
	return st.answer(read, nil, errType, isduStatusDevice(st.mode)), ua.StatusOK
}

// index and subindex are the first two arguments: a UInt16 and a Byte.
func isduIndex(args []*ua.Variant) (int, int, bool) {
	if len(args) < 2 {
		return 0, 0, false
	}
	i, ok1 := args[0].Value().(uint16)
	s, ok2 := args[1].Value().(uint8)
	return int(i), int(s), ok1 && ok2
}

// param resolves an (index, subindex) to the IODD's parameter, or the
// ErrorType a device would answer.
func (st *isduStore) param(idx, sub int) (*iolink.Param, uint16) {
	ref := strconv.Itoa(idx)
	if sub != 0 {
		ref += "." + strconv.Itoa(sub)
	}
	prm, err := iolink.FindParam(st.iodd, ref)
	if err != nil {
		for _, v := range st.iodd.Variables {
			if v.Index == idx {
				return nil, errSubindexNotAvailable
			}
		}
		return nil, errIndexNotAvailable
	}
	return prm, 0
}

func (st *isduStore) read(args []*ua.Variant) ([]*ua.Variant, ua.StatusCode) {
	idx, sub, ok := isduIndex(args)
	if !ok || len(args) != 2 {
		return nil, ua.StatusBadInvalidArgument
	}
	prm, e := st.param(idx, sub)
	if prm == nil {
		return st.refuse(true, e)
	}
	if !prm.CanRead() {
		return st.refuse(true, errAccessDenied)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	raw, ok := st.raw[key(idx, sub)]
	if !ok {
		raw = defaultOctets(prm)
	}
	return st.answer(true, raw, 0, 0), ua.StatusOK
}

func (st *isduStore) write(args []*ua.Variant) ([]*ua.Variant, ua.StatusCode) {
	idx, sub, ok := isduIndex(args)
	if !ok || len(args) != 3 {
		return nil, ua.StatusBadInvalidArgument
	}
	var data []byte
	switch x := args[2].Value().(type) {
	case []byte:
		data = x
	case ua.ByteArray:
		data = x
	case string:
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(x, "0x"), "0X"))
		if err != nil {
			return nil, ua.StatusBadInvalidArgument
		}
		data = b
	default:
		return nil, ua.StatusBadTypeMismatch
	}
	if len(data) > st.max {
		return st.refuse(false, errLengthOverrun)
	}
	prm, e := st.param(idx, sub)
	if prm == nil {
		return st.refuse(false, e)
	}
	if !prm.CanWrite() {
		return st.refuse(false, errAccessDenied)
	}
	switch {
	case len(data) > prm.Octets():
		return st.refuse(false, errLengthOverrun)
	case len(data) < prm.Octets():
		return st.refuse(false, errLengthUnderrun)
	}
	// What the device would hold must be a value its IODD allows.
	if prm.CanRead() {
		v, err := prm.Decode(data)
		if err == nil {
			_, err = prm.Encode(v)
		}
		if err != nil {
			return st.refuse(false, errValueOutOfRange)
		}
	}
	st.mu.Lock()
	st.raw[key(idx, sub)] = append([]byte(nil), data...)
	st.mu.Unlock()
	return st.answer(false, nil, 0, 0), ua.StatusOK
}

// defaultOctets is a parameter's value before anything is written: the
// IODD's default where it parses, zeros where it has none.
func defaultOctets(prm *iolink.Param) []byte {
	zero := make([]byte, prm.Octets())
	def := prm.Variable.Default
	if prm.Item != nil || def == "" {
		return zero
	}
	var v any
	switch prm.Type().Kind {
	case iodd.UInteger:
		n, err := strconv.ParseUint(def, 10, 64)
		if err != nil {
			return zero
		}
		v = n
	case iodd.Integer:
		n, err := strconv.ParseInt(def, 10, 64)
		if err != nil {
			return zero
		}
		v = n
	case iodd.Boolean:
		v = def == "true" || def == "1"
	case iodd.String:
		v = def
	default:
		return zero
	}
	raw, err := prm.Encode(v)
	if err != nil {
		// A read-only variable cannot be encoded for a write: build it with a
		// writable copy of the same type.
		cp := *prm
		cp.Variable.Access = "rw"
		if raw, err = cp.Encode(v); err != nil {
			return zero
		}
	}
	return raw
}

// hasISDUWrite says the instance's master has a WriteISDU to serve: its wire
// face then takes writes as a device does in its default setup (a lab's
// anonymous session on None), as it does for writable points.
func (s *Set) hasISDUWrite(d Instance) bool {
	p := s.Profile(d.Name)
	return len(d.Ports) > 0 && p != nil && p.IOLink != nil && p.IOLink.ISDU.Offers() && p.IOLink.ISDU.Write != ""
}
