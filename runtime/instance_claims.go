package runtime

import (
	"fmt"
	"sort"
	"strings"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// No instance key is dropped without a word (#1458 slice 5, hard condition
// 1 of docs/design/one-struct-model.md §18). A driver that declares the tags
// it owns (InputNames/OutputNames, as every field driver and io.Multi do)
// must take every to: and both: leaf an instance pushes and deliver every
// from: and both: member it reads. Otherwise a command would vanish at the
// driver (a single driver skips a key it has no binding for, and io.Multi
// routes it nowhere) and a reading would never arrive, with nothing said.
// So New refuses the instance, naming the member and the key. The keys are
// the runtime's own (pushLeaf.key, instKey.key), spelled as the TYPE spells
// the member, so a binding spelled otherwise is caught here too.
//
// The other way round as well: a driver binding under an instance's name
// that is none of its keys ("P101.Levels[1]" bound as an output when that
// member is memory) would be written by nothing or delivered to nothing, so
// it is refused too. A delivery is taken only under a key or the instance's
// own name (deliverLocked), so nothing else counts.
//
// A driver that declares nothing (io.Memory, a loopback) owns whatever is
// written to it, so there is nothing to compare. A replacement driver
// (ReplaceDriver) must declare the same tags as the one it replaces, so the
// check made here holds for it.

// instanceKeysClaimed is that check. Caller is New, after registerInstances.
func (t *Tags) instanceKeysClaimed(d nio.Driver) error {
	if len(t.instList) == 0 || d == nil {
		return nil
	}
	o, ok := d.(tagNames)
	if !ok {
		return nil
	}
	outs := map[string]bool{}
	for _, n := range o.OutputNames() {
		outs[n] = true
	}
	ins := map[string]bool{}
	for _, n := range o.InputNames() {
		ins[n] = true
	}
	var problems []string
	for _, in := range t.instList {
		for _, l := range in.push {
			if outs[l.key] {
				continue
			}
			what := "command"
			if l.both {
				what = "peer member"
			}
			problems = append(problems, fmt.Sprintf("instance %s: %s %s (pushed as %q) reaches no driver: no driver takes %q as an output, so every write to it would be dropped%s",
				in.name, what, l.node.path, l.key, l.key, near(l.key, outs)))
		}
		for _, k := range in.keys {
			if ins[k.key] || ins[in.name] {
				continue
			}
			problems = append(problems, fmt.Sprintf("instance %s: member %s (delivered as %q) comes from no driver: no driver delivers %q, so it would never be read%s",
				in.name, k.node.path, k.key, k.key, near(k.key, ins)))
		}
	}
	pushKeys, delKeys := map[string]*instInfo{}, map[string]*instInfo{}
	for _, in := range t.instList {
		for _, l := range in.push {
			pushKeys[l.key] = in
		}
		for _, k := range in.keys {
			delKeys[k.key] = in
		}
		delKeys[in.name] = in
	}
	for _, n := range sortedNames(outs) {
		if in := t.instanceUnder(n); in != nil && pushKeys[n] == nil {
			problems = append(problems, fmt.Sprintf("instance %s: a driver takes %q as an output, which is no to: or both: member of the instance, so nothing would ever be sent through it (%s)", in.name, n, in.kindNote(n)))
		}
	}
	for _, n := range sortedNames(ins) {
		if in := t.instanceUnder(n); in != nil && delKeys[n] == nil {
			problems = append(problems, fmt.Sprintf("instance %s: a driver delivers %q, which is no from: or both: member of the instance, so the reading would go nowhere (%s)", in.name, n, in.kindNote(n)))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

// instanceUnder is the instance a binding name lies under ("P101.Speed",
// "p101[2]"), or nil. The instance's own name is not under it.
func (t *Tags) instanceUnder(name string) *instInfo {
	i := strings.IndexAny(name, ".[")
	if i <= 0 {
		return nil
	}
	return t.instanceOf(name[:i])
}

// kindNote says what a path under an instance is, for a refusal.
func (in *instInfo) kindNote(name string) string {
	i := strings.IndexAny(name, ".[")
	segs := ir.PathSegments(strings.TrimPrefix(name[i:], "."))
	chain, ok := in.walk(segs)
	if !ok {
		return "the type has no such member"
	}
	n := chain[len(chain)-1]
	if !n.leaf {
		return "a struct or array: bind its members one by one"
	}
	return "it is " + n.kind.String()
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// near names a driver's binding that differs from key only in case: the
// usual cause, a binding spelled as the YAML spells the member rather than
// as the TYPE does.
func near(key string, names map[string]bool) string {
	for n := range names {
		if strings.EqualFold(n, key) {
			return fmt.Sprintf(" (a driver has %q: a key is spelled as the TYPE spells the member)", n)
		}
	}
	return ""
}
