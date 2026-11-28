package runtime

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/ir"
)

// WRITTEN(<path>) in an expr: member (#1458 slice 8c; one-struct-model §10):
// TRUE once the to: leaf at path has been written in this leadership, by
// the program, an outside write or a force (the push rules' written,
// instance_push.go). It is how an expression guards a read-back of another
// type, which does not adopt. The path resolves when the stage compiles, to
// one push leaf; each scan's snapshot copies the leaves' flags, so a unit
// reads a bool by index. The stage runs before the program, so a write the
// program makes shows on the next scan.

// writtenRef is one leaf a WRITTEN() asks about: in.push[leaf].
type writtenRef struct {
	in   *instInfo
	leaf int
}

// writtenLeaf resolves WRITTEN's path below the instance to its push leaf,
// refusing anything that is not one to: leaf. The message names the member
// by its path in the type, so it reads the same for every instance of it.
func (in *instInfo) writtenLeaf(segs []string) (int, error) {
	typ := in.typName
	chain, ok := in.walk(segs)
	n := chain[len(chain)-1]
	if !ok {
		return 0, fmt.Errorf("%s has no member %s", typ, joinSegs(segs))
	}
	if n == &in.root {
		return 0, fmt.Errorf("it names the instance %s, not one of its to: members%s", in.name, in.writtenHint())
	}
	if !n.leaf {
		return 0, fmt.Errorf("%s of %s is not one leaf: name one to: member%s", n.path, typ, in.writtenHint())
	}
	if n.kind != MemberTo {
		a := "a"
		if n.kind == MemberExpr {
			a = "an"
		}
		return 0, fmt.Errorf("%s of %s is %s %s: member, and WRITTEN tells whether a to: member has been written in this leadership%s", n.path, typ, a, n.kind, in.writtenHint())
	}
	for i := range in.push {
		if in.push[i].node == n {
			return i, nil
		}
	}
	return 0, fmt.Errorf("internal: no push leaf for %s", n.path)
}

// writtenHint names a to: leaf of the instance's type, as the fix (by its
// member path: the path may name another instance than the expression's).
func (in *instInfo) writtenHint() string {
	for i := range in.push {
		if !in.push[i].both {
			return "; " + in.push[i].node.path + " of " + in.typName + " is one"
		}
	}
	return "; " + in.typName + " has no to: member"
}

func joinSegs(segs []string) string {
	out := ""
	for i, s := range segs {
		if i > 0 && (len(s) == 0 || s[0] != '[') {
			out += "."
		}
		out += s
	}
	return out
}

// writtenResolver resolves each WRITTEN path against the instances lookup
// finds, giving each call its own id; refs collects them.
func writtenResolver(lookup func(root string) (*instInfo, error), refs *[]writtenRef) func(string) (int, error) {
	return func(path string) (int, error) {
		root, segs := ir.SplitAddress(path)
		in, err := lookup(root)
		if err != nil {
			return 0, err
		}
		if in == nil {
			return 0, fmt.Errorf("%s is no member of the expression's instance and no instance: WRITTEN takes a to: member of an instance, such as WRITTEN(Cmd.SpeedRef)", root)
		}
		leaf, err := in.writtenLeaf(segs)
		if err != nil {
			return 0, err
		}
		*refs = append(*refs, writtenRef{in: in, leaf: leaf})
		return len(*refs) - 1, nil
	}
}

// WrittenResolver is the st.LowerOpts.Written that naut check compiles the
// expr: members with: the stage's own rules, from the tag definitions.
// Experimental, as InstanceTag is.
func WrittenResolver(defs []TagDef, types map[string]*ir.Type) func(string) (int, error) {
	built := map[string]*instInfo{}
	var refs []writtenRef
	return writtenResolver(func(root string) (*instInfo, error) {
		k := ir.NameKey(root)
		if in, ok := built[k]; ok {
			return in, nil
		}
		for _, d := range defs {
			if d.Instance == nil || ir.NameKey(d.Name) != k {
				continue
			}
			in, err := newInstInfo(d, types)
			if err != nil {
				return nil, err
			}
			built[k] = in
			return in, nil
		}
		return nil, nil
	}, &refs)
}
