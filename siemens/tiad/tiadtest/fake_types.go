package tiadtest

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// PLC data types (D1b, #1301): a source that declares data types only
// generates into the PLC data types' user group named like the block group
// (the agent's Ownership.TypeSources, TIA V21's type-group overload). TIA
// keeps one name for blocks and data types, and regenerates a same-named
// type where it sits, handing it back with no error (TIA VM, 2026-11-24):
// the agent reads blocks and data types as one list.

// TypesScript scripts the fake TIA's data types (Target.SetTypes).
type TypesScript struct {
	// Foreign are the customer's data types every project holds (name
	// and group path from the data types' root, "" for the root).
	Foreign []ForeignType `json:"foreign,omitempty"`
	// Hidden leaves them out of the agent's reading of the data types
	// (before and after an import alike), while an import still hits one
	// as TIA does: only the glue's check of the generated object's group
	// sees it, so the session must stay unusable on that alone.
	Hidden bool `json:"hidden,omitempty"`
	// CompileTouchesForeign makes a compile move every customer type's
	// modified date (a customer type that uses one of nautilus's).
	CompileTouchesForeign bool `json:"compileTouchesForeign,omitempty"`
	// ImportTouchesForeign makes an import move every customer type's
	// modified date without handing it back: only the agent's check
	// after the import sees it.
	ImportTouchesForeign bool `json:"importTouchesForeign,omitempty"`
	// CompareDropsTypes leaves the data types out of the compare tree
	// (tiad-fake only: the Go fake's compare models no tree).
	CompareDropsTypes bool `json:"compareDropsTypes,omitempty"`
}

// ForeignType is one customer data type.
type ForeignType struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// SetTypes scripts the data types (TypesScript).
func (f *Fake) SetTypes(ts TypesScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.types = ts
}

// projectTypes is the agent's ITia.Types on the fake project: the
// customer's (none with Hidden), then
// every type an import made, in its group.
func (f *Fake) projectTypes(s *session) []projectBlock {
	var l []projectBlock
	if !f.types.Hidden {
		for _, ft := range f.types.Foreign {
			m := s.typeModified[strings.ToLower(ft.Name)]
			if m == "" {
				m = "customer"
			}
			l = append(l, projectBlock{name: ft.Name, kind: "UDT", group: ft.Group, dataType: true, modified: m})
		}
	}
	for _, b := range s.blocks {
		if b.Kind != "UDT" {
			continue
		}
		k := strings.ToLower(b.Name)
		l = append(l, projectBlock{name: b.Name, kind: "UDT", group: s.groups[k], dataType: true, modified: s.typeModified[k]})
	}
	return l
}

// objects is the agent's Objects: blocks, then data types.
func (f *Fake) objects(s *session) []projectBlock {
	return append(s.projectBlocks(), f.projectTypes(s)...)
}

// mixedRefused is Ownership.MixedRefused.
func mixedRefused(ds []declared) *failure {
	types, blocks := map[string]bool{}, map[string]bool{}
	for _, d := range ds {
		if d.Kind == "UDT" {
			types[d.source] = true
		} else {
			blocks[d.source] = true
		}
	}
	var mixed []string
	for _, d := range ds {
		if types[d.source] && blocks[d.source] && !slices.Contains(mixed, d.source) {
			mixed = append(mixed, d.source)
		}
	}
	if len(mixed) == 0 {
		return nil
	}
	return fail(http.StatusBadRequest, tiad.KindBadRequest,
		"%s declares a data type and a block: put the data types (TYPE ... END_TYPE) in sources of their own. Nothing was imported", strings.Join(mixed, ", "))
}

// pruneRefused is Ownership.PruneRefused.
func pruneRefused(objects []projectBlock, ds []declared, group, prefix string) *failure {
	if prefix == "" {
		return nil
	}
	named := map[string]bool{}
	for _, d := range ds {
		named[strings.ToLower(d.Name)] = true
	}
	var not []string
	for _, b := range objects {
		if !owned(b.group, group) || named[strings.ToLower(b.name)] || strings.HasPrefix(strings.ToLower(b.name), strings.ToLower(prefix)) {
			continue
		}
		not = append(not, b.label()+" in "+b.where())
	}
	if len(not) == 0 {
		return nil
	}
	sort.Strings(not)
	return fail(http.StatusConflict, tiad.KindForeignBlock,
		"%s: not named with the prefix %q, so not nautilus's, and the prune would delete it. Nothing was imported. Move it out of group %q in the project, deploy into another group, or deploy with the block prefix that made it",
		strings.Join(not, "; "), prefix, group)
}

// outsideGroup is Ownership.OutsideGroupError for a data type TIA
// regenerated where it sits.
func outsideGroup(source string, ft ForeignType, group string) *failure {
	in := "the data types' root"
	if ft.Group != "" {
		in = fmt.Sprintf("%q", ft.Group)
	}
	return fail(http.StatusInternalServerError, tiad.KindOperationFailed,
		"TIA: %s: TIA generated data type %q outside data types group %q, in %s: the import may have changed an object outside group %q, so this session can no longer be saved, archived for a deploy or downloaded. Close it without saving, then open or adopt the project again, and rename the source's object (a prefix the project does not use)",
		source, ft.Name, group, in, group)
}

// compileChanged is Ownership.CompileChangedError.
func compileChanged(changed []string, group string) *failure {
	return fail(http.StatusConflict, tiad.KindUncleanSession,
		"the compile changed program blocks or data types outside group %q (%s): they are not nautilus's, and tiad does not recompile them. Most likely they use a block or data type of group %q, or the project was not compiled as found. This session can no longer be saved, archived for a deploy or downloaded: close it without saving. A deploy stays refused while the project's own objects use group %q or need a compile; that needs the project changed outside nautilus",
		group, strings.Join(changed, "; "), group, group)
}
