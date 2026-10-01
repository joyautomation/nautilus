// Package profiles is the SNMP driver's MIB knowledge, spent at import time
// only (docs/design/it-drivers.md §3): which columns of IF-MIB make a
// SwitchPort, which CPS-MIB objects make a PDU. A profile reads a walk — a
// live one or its recording, the same bytes — enumerates the instances
// (ifIndex rows, outlet rows, sensor rows) and returns explicit per-member
// bindings. The driver never sees a profile: at run time it executes OIDs.
//
// Every OID here is cited: the MIB module and object name sit beside it,
// and the three non-switch profiles were written from the published MIB
// files (RFC 1628 UPS-MIB; CyberPower's CPS-MIB as distributed by LibreNMS,
// mibs/cyberpower/CPS-MIB) and are UNVERIFIED against hardware until a walk
// of a real device is recorded (brief §12.2). An object that cannot be
// cited is not bound — the member stays at zero-of-field and import says so.
package profiles

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// Instance is one struct tag a profile generates: the name suffix after the
// device prefix ("" for the device root, "_Port03", "_Temp_CPU"), the
// contract type, and its bindings.
type Instance struct {
	Suffix  string
	Type    string
	Members map[string]snmp.Member
}

// Result is one profile's expansion of one walk.
type Result struct {
	Profile   string
	Instances []Instance
	// Notes are what import prints for the reviewer: members left at zero
	// because the walk lacks their object, vendor hooks, assumptions.
	Notes []string
}

// Options steer an expansion.
type Options struct {
	// Ports selects interfaces by ifIndex; nil means the physical ethernet
	// ports (ifType ethernetCsmacd(6)). Only the switch profile reads it.
	Ports *IndexSet
}

// Profile is one device family.
type Profile struct {
	Name string
	Desc string
	// Verified is false for a profile written from the published MIB and
	// not yet checked against a walk of the real hardware.
	Verified bool
	// Probe reports whether a walk looks like this profile's device, for
	// the pick when sysObjectID names no profile.
	Probe func(w walk.Walk) bool
	Build func(w walk.Walk, o Options) (Result, error)
}

// All lists the profiles, in pick order.
func All() []Profile {
	return []Profile{switchProfile(), pduCyberPower(), upsCyberPower(), upsRFC1628()}
}

// Names lists the profile names, for usage text and errors.
func Names() []string {
	var out []string
	for _, p := range All() {
		out = append(out, p.Name)
	}
	return out
}

// byEnterprise picks a profile by sysObjectID prefix first. Only prefixes
// that are enterprise numbers (IANA-assigned, citable) are listed; within
// an enterprise the content probe decides (CyberPower makes both PDUs and
// UPSes under 3808).
var byEnterprise = []struct {
	prefix   string
	profiles []string
}{
	{"1.3.6.1.4.1.3808", []string{"pdu-cyberpower", "ups-cyberpower"}}, // CyberPower Systems (CPS-MIB)
	{"1.3.6.1.4.1.52642", []string{"switch"}},                          // FS.COM (FSOS)
}

// Pick chooses the profile for a walk: the named one when name is set
// (--profile), else by sysObjectID enterprise, else by probing the walk's
// content. The error lists the profiles when nothing fits.
func Pick(w walk.Walk, name string) (Profile, error) {
	all := All()
	find := func(n string) (Profile, bool) {
		for _, p := range all {
			if p.Name == n {
				return p, true
			}
		}
		return Profile{}, false
	}
	if name != "" {
		if p, ok := find(name); ok {
			return p, nil
		}
		return Profile{}, fmt.Errorf("unknown profile %q (have %s)", name, strings.Join(Names(), ", "))
	}
	if vb, ok := w.Get(sysObjectID); ok && vb.Type == walk.ObjectID {
		for _, e := range byEnterprise {
			if walk.HasPrefix(vb.Str, e.prefix) {
				for _, n := range e.profiles {
					if p, _ := find(n); p.Probe(w) {
						return p, nil
					}
				}
			}
		}
	}
	for _, p := range all {
		if p.Probe(w) {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("no profile recognises this device (sysObjectID %s) — pass --profile (%s)",
		stringAt(w, sysObjectID), strings.Join(Names(), ", "))
}

// Subtrees are the OID subtrees the profiles read: what a live import or a
// default browse walks, so a live import and an import of its recording
// see the same varbinds.
func Subtrees() []string {
	return []string{
		"1.3.6.1.2.1.1",          // SNMPv2-MIB system
		"1.3.6.1.2.1.2",          // IF-MIB interfaces (ifNumber, ifTable)
		"1.3.6.1.2.1.17.1.4.1",   // BRIDGE-MIB dot1dBasePortTable (bridge port → ifIndex)
		"1.3.6.1.2.1.17.7.1.4.3", // Q-BRIDGE-MIB dot1qVlanStaticTable (not the FDB: it is big)
		"1.3.6.1.2.1.17.7.1.4.5", // Q-BRIDGE-MIB dot1qPortVlanTable (PVID)
		"1.3.6.1.2.1.31.1.1",     // IF-MIB ifXTable
		"1.3.6.1.2.1.33",         // UPS-MIB (RFC 1628)
		"1.3.6.1.2.1.47.1.1.1",   // ENTITY-MIB entPhysicalTable
		"1.3.6.1.2.1.99.1.1",     // ENTITY-SENSOR-MIB entPhySensorTable
		"1.3.6.1.2.1.105.1.1",    // POWER-ETHERNET-MIB pethPsePortTable
		"1.3.6.1.4.1.3808.1.1",   // CPS-MIB hardware (CyberPower UPS, ePDU)
		"1.3.6.1.4.1.52642",      // FS.COM enterprise (vendor hook; see fs.go)
	}
}

// ── shared helpers ─────────────────────────────────────────────────────

const (
	sysDescr    = "1.3.6.1.2.1.1.1.0" // SNMPv2-MIB::sysDescr.0
	sysObjectID = "1.3.6.1.2.1.1.2.0" // SNMPv2-MIB::sysObjectID.0
	sysUpTime   = "1.3.6.1.2.1.1.3.0" // SNMPv2-MIB::sysUpTime.0 (TimeTicks, 1/100 s)
	sysName     = "1.3.6.1.2.1.1.5.0" // SNMPv2-MIB::sysName.0
)

func stringAt(w walk.Walk, oid string) string {
	vb, ok := w.Get(oid)
	if !ok {
		return "(absent)"
	}
	return vb.ValueString()
}

func textAt(w walk.Walk, oid string) (string, bool) {
	vb, ok := w.Get(oid)
	if !ok || vb.Type != walk.OctetString {
		return "", false
	}
	return strings.TrimRight(string(vb.Bytes), "\x00"), true
}

func intAt(w walk.Walk, oid string) (int64, bool) {
	vb, ok := w.Get(oid)
	if !ok {
		return 0, false
	}
	switch vb.Type {
	case walk.Integer:
		return vb.Int, true
	case walk.Counter32, walk.Gauge32, walk.TimeTicks, walk.Counter64:
		return int64(vb.Uint), true
	}
	return 0, false
}

func has(w walk.Walk, oid string) bool {
	_, ok := w.Get(oid)
	return ok
}

// rows lists the instance suffixes under a column ("1", "2", "1.3"), in
// walk order.
func rows(w walk.Walk, column string) []string {
	var out []string
	for _, vb := range w.Subtree(column) {
		out = append(out, strings.TrimPrefix(vb.OID, column+"."))
	}
	return out
}

// bind is a polled member binding.
func bind(oid string, b hw.Binding) snmp.Member { return snmp.Member{OID: oid, Binding: b} }

// cnst is a const member.
func cnst(v any) snmp.Member { return snmp.Member{Binding: hw.Binding{Const: v}} }

// derived is a derived member.
func derived(expr string) snmp.Member { return snmp.Member{Binding: hw.Binding{Derived: expr}} }

// member binds oid when the walk carries it, else notes the gap.
type builder struct {
	w     walk.Walk
	notes []string
}

func (b *builder) opt(members map[string]snmp.Member, member, oid string, bd hw.Binding, what string) bool {
	if !has(b.w, oid) {
		b.notes = append(b.notes, fmt.Sprintf("%s: %s not in the walk (%s) — left at zero", member, what, oid))
		return false
	}
	members[member] = bind(oid, bd)
	return true
}

// Sanitise maps a device-supplied name to the tag alphabet [A-Za-z0-9_]:
// runs of anything else become one underscore, and leading/trailing
// underscores go. "CPU 0 (core)" → "CPU_0_core".
func Sanitise(s string) string {
	s = nonTag.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

var nonTag = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// padWidth is the digit count of the largest index — Port01…Port28, Fan1…Fan6.
func padWidth(max int) int {
	return len(strconv.Itoa(max))
}

func padded(n, width int) string {
	return fmt.Sprintf("%0*d", width, n)
}

// IndexSet is a --ports selection: "1-24,26", or "all".
type IndexSet struct {
	All    bool
	ranges [][2]int
}

// ParseIndexSet reads "1-24,26,28" or "all".
func ParseIndexSet(s string) (*IndexSet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if s == "all" {
		return &IndexSet{All: true}, nil
	}
	set := &IndexSet{}
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 {
			return nil, fmt.Errorf("--ports %q: %q is not an index", s, part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("--ports %q: %q is not a range", s, part)
			}
		}
		set.ranges = append(set.ranges, [2]int{a, b})
	}
	return set, nil
}

// Has reports whether index i is selected.
func (s *IndexSet) Has(i int) bool {
	if s.All {
		return true
	}
	for _, r := range s.ranges {
		if i >= r[0] && i <= r[1] {
			return true
		}
	}
	return false
}

// String renders the set the way it was given, normalised.
func (s *IndexSet) String() string {
	if s.All {
		return "all"
	}
	parts := make([]string, len(s.ranges))
	for i, r := range s.ranges {
		if r[0] == r[1] {
			parts[i] = strconv.Itoa(r[0])
		} else {
			parts[i] = fmt.Sprintf("%d-%d", r[0], r[1])
		}
	}
	return strings.Join(parts, ",")
}

func sortInts(xs []int) { sort.Ints(xs) }
