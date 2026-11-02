package st

import (
	"errors"
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Hooks for project composition: what a manifest tag's `type:` means, and
// which globals a GVL file declares. They sit beside the lowerer, not in
// it, and reuse its type resolution so a tag's type and a VAR_EXTERNAL's
// can never be read two different ways.

// Types resolves the TYPE declarations of a parsed source — its own TYPE
// blocks, which in a composed source include every library's — without
// lowering any POU. The result keys each TYPE by its declared spelling.
//
// Each FUNCTION_BLOCK the source declares is in the result too, as a
// TypeFB with no FBDef, after the TYPEs resolve (so no TYPE can reach
// one): enough for ResolveTypeName to refuse a project block by name, as
// it refuses a standard one. Callers that want only the TYPEs skip
// Kind == ir.TypeFB.
func Types(prog *Program) (map[string]*ir.Type, error) {
	l := newLowerer(prog, nil)
	// The same sequence LowerWithOpts runs: shells and enumerations, the
	// project constants (an array bound may name one), then the rest.
	if err := l.collectTypeShells(); err != nil {
		return nil, err
	}
	if err := l.collectGlobalConsts(); err != nil {
		return nil, err
	}
	if err := l.collectTypes(); err != nil {
		return nil, err
	}
	for _, fb := range prog.FBDecls {
		if _, _, dup := ir.Lookup(l.types, fb.Name); !dup {
			l.types[fb.Name] = &ir.Type{Kind: ir.TypeFB, Name: fb.Name}
		}
	}
	return l.types, nil
}

// ErrFunctionBlockType is what ResolveTypeName's refusal of a
// function-block type wraps, standard (TON) or project-declared, whether
// the type is the block itself or holds one (an ARRAY OF TON, a STRUCT
// with a TON member).
var ErrFunctionBlockType = errors.New("a function block — an instance is program state, not a tag")

// fbInsideError is the refusal of a type that holds a function block
// rather than naming one: the message names the block, and errors.Is
// still finds ErrFunctionBlockType.
type fbInsideError struct{ msg string }

func (e *fbInsideError) Error() string { return e.msg }
func (e *fbInsideError) Unwrap() error { return ErrFunctionBlockType }

// ResolveTypeName resolves a type as a manifest writes it — an IEC
// elementary type (`INT`, `REAL`, `TIME`, `STRING`), a TYPE
// the project declares (`Motor`), or an array of either
// (`ARRAY[1..4] OF REAL`) — against types, the project's TYPE table. A
// function-block type is refused: an FB instance is program state, never a
// tag. So is a type that holds one at any depth — an ARRAY OF TON, an
// array of a project block, a STRUCT with a TON member.
func ResolveTypeName(name string, types map[string]*ir.Type) (*ir.Type, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("no type")
	}
	prog, err := Parse("PROGRAM __tagtype\nVAR\n    __t : " + name + ";\nEND_VAR\nEND_PROGRAM\n")
	if err != nil || len(prog.VarBlocks) != 1 || len(prog.VarBlocks[0].Variables) != 1 {
		return nil, fmt.Errorf("%q is not a type", name)
	}
	l := newLowerer(prog, nil)
	l.types = types
	if l.types == nil {
		l.types = map[string]*ir.Type{}
	}
	t, err := l.resolveType(prog.VarBlocks[0].Variables[0].Type)
	if err != nil {
		return nil, err
	}
	if t.Kind == ir.TypeFB {
		return nil, fmt.Errorf("%s is %w", name, ErrFunctionBlockType)
	}
	if err := FunctionBlockIn(t); err != nil {
		return nil, fmt.Errorf("%s — %w", name, err)
	}
	return t, nil
}

// FunctionBlockIn reports whether t is or holds a function block — an
// array of blocks at any nesting, a STRUCT with a block member: nil when
// it holds none, else an error naming the block (`TON is a function
// block; …`, or `member Delay is a TON, a function block; …`) that
// errors.Is finds as ErrFunctionBlockType. The caller prefixes what
// declared the type.
func FunctionBlockIn(t *ir.Type) error {
	fb, member := fbInside(t, "", map[*ir.StructDef]bool{})
	switch {
	case fb == "":
		return nil
	case member == "":
		return &fbInsideError{fb + " is a function block; an instance is program state, not a tag"}
	}
	return &fbInsideError{fmt.Sprintf(
		"member %s is a %s, a function block; an instance is program state, not a tag", member, fb)}
}

// TypeSpelling writes a resolved type the way a declaration spells it —
// `ARRAY[1..2] OF TON`, a block's or a TYPE's name — for a diagnostic. A
// multi-dimensional array reads as nested arrays, which is how it
// resolves.
func TypeSpelling(t *ir.Type) string {
	switch {
	case t == nil:
		return "?"
	case t.Kind == ir.TypeArray:
		return fmt.Sprintf("ARRAY[%d..%d] OF %s", t.ArrLoBound, t.ArrLoBound+t.ArrLen-1, TypeSpelling(t.Elem))
	case t.Kind == ir.TypeFB && t.Name == "" && t.FB != nil:
		return t.FB.Name
	}
	return t.String()
}

// fbInside finds a function block t holds — as an array's element or a
// struct's member, at any depth — and returns its name and, when it sits
// under a struct, the member path to it (`Delay`, `Stages.Delay`). An
// array's element is reached without a path step: every element holds the
// same type. Empty when t holds none.
func fbInside(t *ir.Type, path string, seen map[*ir.StructDef]bool) (fb, member string) {
	switch {
	case t == nil:
		return "", ""
	case t.Kind == ir.TypeFB:
		if t.Name == "" && t.FB != nil {
			return t.FB.Name, path
		}
		return t.Name, path
	case t.Kind == ir.TypeArray:
		return fbInside(t.Elem, path, seen)
	case t.Kind == ir.TypeStruct && t.Struct != nil && !seen[t.Struct]:
		seen[t.Struct] = true
		for _, f := range t.Struct.Fields {
			p := f.Name
			if path != "" {
				p = path + "." + f.Name
			}
			if fb, member := fbInside(f.Type, p, seen); fb != "" {
				return fb, member
			}
		}
	}
	return "", ""
}

// FileGlobal is one variable a file-level VAR_GLOBAL block (a GVL)
// declares.
type FileGlobal struct {
	Name     string
	Datatype string
	Constant bool
	// Retain: the block is VAR_GLOBAL RETAIN (or PERSISTENT), so the global
	// survives a restart in the retain store.
	Retain bool
	Pos    Pos
}

// FileGlobals lists the globals a parsed source's file-level VAR_GLOBAL
// blocks declare, in source order.
func FileGlobals(prog *Program) []FileGlobal {
	var out []FileGlobal
	for _, vb := range prog.VarBlocks {
		if !vb.FileScope {
			continue
		}
		for _, vd := range vb.Variables {
			out = append(out, FileGlobal{Name: vd.Name, Datatype: vd.Datatype, Constant: vb.Constant, Retain: vb.Retain, Pos: vd.Pos})
		}
	}
	return out
}
