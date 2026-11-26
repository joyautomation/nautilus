package devprofile

import (
	"fmt"
	"sort"
	"strings"
)

// Finding is one thing `naut devices lint` reports about a profile that
// already parses. ParseProfile refuses what would make a profile unusable
// (a missing citation, a derived point of nothing, a non-BOOL alarm); a
// Finding is what a reader of the profile should still fix or look at.
type Finding struct {
	// Error findings make lint fail; warnings do not.
	Error bool
	// Point is the row the finding is about, or "".
	Point string
	Msg   string
}

func (f Finding) String() string {
	lvl := "warning"
	if f.Error {
		lvl = "error"
	}
	if f.Point != "" {
		return fmt.Sprintf("%s: %s: %s", lvl, f.Point, f.Msg)
	}
	return fmt.Sprintf("%s: %s", lvl, f.Msg)
}

// Lint checks a parsed profile for what writing one from a manual gets
// wrong: two points on one address, a citation that names no place, a document nothing cites or
// that cannot be found again. The simulator is compiled by the caller
// (it needs the language packages, which this leaf does not import).
func (p *Profile) Lint() []Finding {
	var out []Finding
	add := func(isErr bool, point, format string, a ...any) {
		out = append(out, Finding{Error: isErr, Point: point, Msg: fmt.Sprintf(format, a...)})
	}

	cited := map[string]bool{}
	cite := func(doc string) {
		if doc != "" {
			id, _, _ := strings.Cut(doc, " ")
			cited[id] = true
		}
	}
	checkPlace := func(name, doc string) {
		if doc != "" && !strings.Contains(strings.TrimSpace(doc), " ") {
			add(false, name, "doc %q names the document but not the page or section the row came from", doc)
		}
	}
	unverified := 0
	for _, pt := range p.Points {
		cite(pt.Doc)
		checkPlace(pt.Name, pt.Doc)
		if pt.Unverified {
			unverified++
		}
	}
	for _, d := range p.Derived {
		cite(d.Doc)
		checkPlace(d.Name, d.Doc)
		if d.Unverified {
			unverified++
		}
	}
	if m := p.Modbus; m != nil {
		cite(m.MaxConnectionsDoc)
	}
	for _, d := range p.Docs {
		switch {
		case !cited[d.ID]:
			add(false, "", "document %q is listed under docs but nothing cites it", d.ID)
		case d.URL == "" && d.Code == "":
			add(false, "", "document %q has neither a url nor a publisher's document number, so nobody can find it again", d.ID)
		}
	}
	if unverified > 0 {
		add(false, "", "%d row(s) are unverified: check each against a document before the profile is relied on (docs/design/device-library.md §4)", unverified)
	}

	out = append(out, p.lintOverlap()...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Error && !out[j].Error })
	return out
}

// lintOverlap finds two points that read or write the same address. A
// BOOL point that takes a bit of a register (format bit:N) legitimately
// shares it with the register's own point, of any width; two of one bit do
// not.
func (p *Profile) lintOverlap() []Finding {
	var out []Finding
	if p.Protocol == ProtoOPCUA {
		seen := map[string]string{}
		for _, pt := range p.Points {
			if pt.NodeID == "" {
				continue
			}
			if prev, ok := seen[pt.NodeID]; ok {
				out = append(out, Finding{true, pt.Name, fmt.Sprintf("node %s is also %s", pt.NodeID, prev)})
			}
			seen[pt.NodeID] = pt.Name
		}
		return out
	}
	type slot struct {
		table string
		addr  int
		bit   string
	}
	owner := map[slot]string{}
	for _, pt := range p.Points {
		bit := ""
		w := pt.Words()
		if strings.HasPrefix(pt.Format, "bit:") {
			bit, w = pt.Format, 1
		}
		for i := 0; i < w; i++ {
			s := slot{pt.Table, int(pt.Address) + i, bit}
			if prev, ok := owner[s]; ok {
				out = append(out, Finding{true, pt.Name, fmt.Sprintf("%s address %d is also %s", pt.Table, int(pt.Address)+i, prev)})
				break
			}
			owner[s] = pt.Name
		}
	}
	return out
}
