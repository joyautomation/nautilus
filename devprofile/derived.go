package devprofile

import (
	"fmt"
	"strings"
)

// Derived is a BOOL member of a device's struct computed from one polled
// point (#717 PR 5a): true when bit Bit of the point is set, or when the
// point equals Eq. Alarms are BOOL-only and many faults are a bit of a word
// or a code, so this is what lets a profile name them. It is never polled,
// written or in a manifest; the adapter computes it each time the point
// arrives, and a virtual device's simulator keeps only the source point.
type Derived struct {
	Name string `yaml:"name"`
	Desc string `yaml:"desc"`
	// From is the polled point it is computed from (an integer point).
	From string `yaml:"from"`
	// Bit is the bit of From that must be set (0 is the least significant).
	// Exactly one of Bit and Eq is given.
	Bit *int `yaml:"bit"`
	// Eq is the value of From that makes it true.
	Eq *int64 `yaml:"eq"`
	// Doc and Unverified cite the row, as a Point's do.
	Doc        string `yaml:"doc"`
	Unverified bool   `yaml:"unverified"`
}

// Eval is the derived point's value for its source point's value v.
func (d Derived) Eval(v int64) bool {
	if d.Bit != nil {
		return uint64(v)>>uint(*d.Bit)&1 == 1
	}
	return d.Eq != nil && v == *d.Eq
}

// intBits is the width of the integer IEC types a derived point may read.
var intBits = map[string]int{"INT": 16, "UINT": 16, "WORD": 16, "DINT": 32, "UDINT": 32, "DWORD": 32}

// validateDerived checks the derived points: a name that is an identifier
// and unused by any point, a citation, a source that is a polled integer
// point, and exactly one of bit (inside the source's width) and eq. seen
// holds the lower-cased point names, and gains the derived ones.
func (p *Profile) validateDerived(seen map[string]bool) error {
	docs := map[string]bool{}
	for _, d := range p.Docs {
		docs[d.ID] = true
	}
	for i := range p.Derived {
		d := &p.Derived[i]
		if !identRE.MatchString(d.Name) {
			return fmt.Errorf("derived %q: the name must be an IEC identifier", d.Name)
		}
		k := strings.ToLower(d.Name)
		if seen[k] {
			return fmt.Errorf("derived %s: the name is already a point or derived point of %s", d.Name, p.ID)
		}
		seen[k] = true
		if d.Doc != "" {
			id, _, _ := strings.Cut(d.Doc, " ")
			if !docs[id] {
				return fmt.Errorf("derived %s: doc %q is not the id of a document under docs", d.Name, id)
			}
		} else if !d.Unverified {
			return fmt.Errorf("derived %s: cite a document (doc:) or mark it unverified: true", d.Name)
		}
		src, ok := p.Point(d.From)
		if !ok {
			return fmt.Errorf("derived %s: from: %q is not a point of %s (a derived point reads a polled point, not another derived one)", d.Name, d.From, p.ID)
		}
		d.From = src.Name
		bits, ok := intBits[strings.ToUpper(src.Type)]
		if !ok {
			return fmt.Errorf("derived %s: from: %s is %s; only an integer point (INT UINT WORD DINT UDINT DWORD) can be tested", d.Name, src.Name, src.Type)
		}
		if (d.Bit == nil) == (d.Eq == nil) {
			return fmt.Errorf("derived %s: give exactly one of bit: and eq:", d.Name)
		}
		if d.Bit != nil && (*d.Bit < 0 || *d.Bit >= bits) {
			return fmt.Errorf("derived %s: bit %d is outside the %d bits of %s (%s)", d.Name, *d.Bit, bits, src.Name, src.Type)
		}
	}
	return nil
}

// Selected is what a device row's points: globs choose: the polled points
// in profile order, and the derived points with their sources.
type Selected struct {
	Points  []Point
	Derived []Derived
}

// SelectAll is Select over the derived points too. A glob matches a
// point's or a derived point's name; selecting a derived point also selects
// the point it reads (the struct has no member the adapter could not
// compute), so the source is polled and a member of the struct.
func (p *Profile) SelectAll(globs []string) (Selected, error) {
	var sel Selected
	byGlob, err := p.selectNames(globs)
	if err != nil {
		return sel, err
	}
	need := map[string]bool{}
	for _, d := range p.Derived {
		if byGlob(d.Name) {
			sel.Derived = append(sel.Derived, d)
			need[strings.ToLower(d.From)] = true
		}
	}
	for _, pt := range p.Points {
		if byGlob(pt.Name) || need[strings.ToLower(pt.Name)] {
			sel.Points = append(sel.Points, pt)
		}
	}
	return sel, nil
}
