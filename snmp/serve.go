// serve.go is the manifest read backwards: for a stand-in agent serving a
// plant simulation (`naut snmp serve --from`), which tag member feeds each
// OID, and the varbind a member's value becomes. The manifest the driver
// polls with is the one description of the device both ways — the same
// OIDs, the same eq/map/scale/rate — so a plant that writes
// SW1_Port25.OperUp := FALSE is read back by the driver as exactly that
// (docs/design/it-drivers.md §8; hw.Binding.Invert is the arithmetic).
package snmp

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// Feed is one OID a source's manifest binds, and the tag member that
// feeds it.
type Feed struct {
	OID     string
	Tag     string
	Member  string
	Field   hw.Field
	Binding hw.Binding
	// Shadowed names the other members bound to the same OID
	// ("SW1_Port01.InErrors" under ErrorRate's rate): they read what this
	// one serves, and are not fed themselves.
	Shadowed []string
}

// Feeds lists every OID source's tags bind, in OID order. When several
// members bind one OID the rate member owns it — the counter is integrated
// from the rate, and an absolute member over the same counter (InErrors
// under ErrorRate) reads the integral; otherwise the first member by name.
// Two rate members on one OID cannot both be served: an error.
func (m Manifest) Feeds(source string) ([]Feed, error) {
	found := false
	for _, s := range m.Sources {
		found = found || s.ID == source
	}
	if !found {
		return nil, fmt.Errorf("snmp: no source %q in the manifest", source)
	}
	byOID := map[string][]Feed{}
	for _, t := range m.Tags {
		if t.Source != source {
			continue
		}
		for _, name := range sortedKeys(t.Members) {
			mb := t.Members[name]
			if mb.IsStatic() {
				continue
			}
			_, f, ok := hw.FieldOf(t.Type, name)
			if !ok {
				return nil, fmt.Errorf("snmp: tag %s: type %s has no member %q", t.Name, t.Type, name)
			}
			oid, err := walk.ParseOID(mb.OID)
			if err != nil {
				return nil, fmt.Errorf("snmp: tag %s: member %s: %w", t.Name, name, err)
			}
			byOID[oid] = append(byOID[oid], Feed{OID: oid, Tag: t.Name, Member: name, Field: f, Binding: mb.Binding})
		}
	}
	out := make([]Feed, 0, len(byOID))
	for _, oid := range sortedKeys(byOID) {
		fs := byOID[oid]
		owner := 0
		rates := 0
		for i, f := range fs {
			if f.Binding.Rate {
				rates++
				owner = i
			}
		}
		if rates > 1 {
			return nil, fmt.Errorf("snmp: %s is bound by %d rate members (%s.%s, …): which one drives the counter?", oid, rates, fs[0].Tag, fs[0].Member)
		}
		feed := fs[owner]
		for i, f := range fs {
			if i != owner {
				feed.Shadowed = append(feed.Shadowed, f.Tag+"."+f.Member)
			}
		}
		out = append(out, feed)
	}
	return out, nil
}

// OnlineTag names the source's root tag — the one whose contract type has
// an Online member (Switch, PDU, UPS). The driver computes Online from
// whether the agent answers, so the manifest never binds it; a stand-in
// reads it the other way: false means stop answering. "" when none.
func (m Manifest) OnlineTag(source string) string {
	for _, t := range m.Tags {
		if t.Source != source {
			continue
		}
		if _, _, ok := hw.FieldOf(t.Type, "Online"); ok {
			return t.Name
		}
	}
	return ""
}

// TagPatterns are /api/state tag filters that cover source's tags with
// as few patterns as the controller allows: "SW1" and "SW1_*" rather than
// 29 names. nil (no filter) past the controller's cap of 40.
func (m Manifest) TagPatterns(source string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range m.Tags {
		if t.Source != source {
			continue
		}
		p := t.Name
		if i := strings.IndexByte(p, '_'); i > 0 {
			p = p[:i+1] + "*"
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		return nil
	}
	return out
}

// Serve is the varbind a device reports for the member to read v, in the
// recorded varbind's type. Not for a rate member (Rate is).
func (f Feed) Serve(v ir.Value, cur walk.Varbind) (walk.Varbind, error) {
	if f.Binding.Rate {
		return walk.Varbind{}, fmt.Errorf("%s.%s: a rate member is served by its counter (Rate)", f.Tag, f.Member)
	}
	raw, ok, err := RawOf(cur)
	if err != nil {
		return walk.Varbind{}, err
	}
	if !ok {
		return walk.Varbind{}, fmt.Errorf("%s: the recording holds no value (%s) to serve in its place", cur.OID, cur.Type)
	}
	out, err := f.Binding.Invert(f.Field, v, raw)
	if err != nil {
		return walk.Varbind{}, fmt.Errorf("%s.%s: %w", f.Tag, f.Member, err)
	}
	return VarbindOf(cur, out)
}

// Rate is the per-second increase of the counter at f.OID that reads as v
// — what the stand-in ramps the counter by. Never negative: counters
// only go up.
func (f Feed) Rate(v ir.Value) (float64, error) {
	if !f.Binding.Rate {
		return 0, fmt.Errorf("%s.%s: not a rate member", f.Tag, f.Member)
	}
	raw, err := f.Binding.Invert(f.Field, v, hw.RawUintVal(0))
	if err != nil {
		return 0, fmt.Errorf("%s.%s: %w", f.Tag, f.Member, err)
	}
	return max(raw.F, 0), nil
}

// VarbindOf is RawOf backwards: raw, encoded as cur's type at cur's OID.
func VarbindOf(cur walk.Varbind, raw hw.Raw) (walk.Varbind, error) {
	out := walk.Varbind{OID: cur.OID, Type: cur.Type}
	switch cur.Type {
	case walk.Integer:
		switch raw.Kind {
		case hw.RawInt:
			out.Int = raw.I
		case hw.RawUint:
			out.Int = int64(raw.U)
		default:
			return out, fmt.Errorf("%s: INTEGER from %q", cur.OID, raw.Key())
		}
	case walk.Counter32, walk.Gauge32, walk.TimeTicks, walk.Counter64:
		switch raw.Kind {
		case hw.RawUint:
			out.Uint = raw.U
		case hw.RawInt:
			out.Uint = uint64(max(raw.I, 0))
		default:
			return out, fmt.Errorf("%s: %s from %q", cur.OID, cur.Type, raw.Key())
		}
		if cur.Type != walk.Counter64 {
			out.Uint &= 1<<32 - 1
		}
	case walk.OctetString, walk.Opaque:
		out.Bytes = []byte(raw.Key())
		if len(cur.Bytes) > 0 && (!walk.Printable(cur.Bytes) || allZero(cur.Bytes)) {
			// A binary string (a MAC, a PortList) is delivered as "00:1A:…":
			// parse it back, padded to the recorded length (a PortList's is
			// fixed). An all-zero recording is binary too: an empty PortList
			// reads as "" off the wire, but it is not text.
			if b, err := hex.DecodeString(strings.ReplaceAll(raw.Key(), ":", "")); err == nil {
				if len(b) < len(cur.Bytes) {
					b = append(b, make([]byte, len(cur.Bytes)-len(b))...)
				}
				out.Bytes = b
			}
		}
	case walk.ObjectID, walk.IPAddress:
		out.Str = raw.Key()
	default:
		return out, fmt.Errorf("%s: cannot serve a %s", cur.OID, cur.Type)
	}
	return out, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
