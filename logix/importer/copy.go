package importer

import (
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// COP and CPS: Logix's block copies, imported when they copy like for
// like. COP(Source, Dest, Length) copies Length elements of Dest's type,
// starting at the Source and Dest elements named; CPS is the same copy
// made uninterruptible, so no I/O update or other task lands mid-copy.
//
// nautilus has no copy instruction and needs none: an assignment copies a
// whole array or structure (`B := A`), and an element-by-element run copies
// a slice. Every nautilus scan already reads its tags as one snapshot and
// commits them as one unit, so each of those copies is as uninterruptible
// as a CPS. What the import must keep is the Logix spelling, so a write
// back gives Studio 5000 the instruction it had:
//
//   - a whole array or structure is a plain assignment, which the writer
//     emits as COP (Logix cannot assign either);
//   - any other copy (a slice, one element, a scalar, a STRING) carries
//     the pragma `(* @logix COP n *)` on its last assignment, n being the
//     run's length (left out for 1), and the writer folds exactly those n
//     assignments back into the one instruction;
//   - a CPS always carries `(* @logix CPS n *)`.
//
// A copy between different types reinterprets bytes (DINTs into a SINT
// buffer for a socket payload); nautilus copies values, not memory, so it
// is refused, as are the copies whose meaning depends on memory layout: a
// length that runs past either end, or source and destination overlapping
// in one array (the result there depends on which way Logix walks).

// maxUnrolled caps a slice copied element by element.
const maxUnrolled = 64

// copyOp is one COP/CPS operand, resolved.
type copyOp struct {
	arg   string // as written, Logix spelling
	array string // the array whose elements are copied, Logix spelling; "" for a single value
	start int    // first element copied
	count int    // the array's element count
	elem  string // element type (the value's own type when array is "")
	whole bool   // the operand names the whole value or array, no trailing index
}

func (rt *routine) copyInstr(in *l5x.Instr) (string, *refusal) {
	m := strings.ToUpper(in.Mnemonic)
	if len(in.Args) != 3 {
		return "", refuse(m, "%d operands", len(in.Args))
	}
	n, err := strconv.Atoi(strings.TrimSpace(in.Args[2]))
	if err != nil {
		return "", refuse(m, "a length that is not a constant (%s)", in.Args[2])
	}
	if n < 1 {
		return "", refuse(m, "a length of %d", n)
	}
	src, bad := rt.copyOperand(m, in.Args[0])
	if bad != nil {
		return "", bad
	}
	dst, bad := rt.copyOperand(m, in.Args[1])
	if bad != nil {
		return "", bad
	}
	if !strings.EqualFold(src.elem, dst.elem) {
		return "", refuse(m, "a copy from %s to %s reinterprets bytes; nautilus copies like for like", src.elem, dst.elem)
	}
	switch strings.ToUpper(dst.elem) {
	case "TIMER", "COUNTER", "MESSAGE", "CONTROL":
		return "", refuse(m, "a copy of %s structures", strings.ToUpper(dst.elem))
	}
	if _, ok := rt.im.typeText(dst.elem, ""); !ok {
		return "", refuse("type", "%s: type %s has no nautilus declaration", in.Args[1], dst.elem)
	}
	pragma := func(n int) string {
		if n == 1 {
			return " (* @logix " + m + " *)"
		}
		return " (* @logix " + m + " " + strconv.Itoa(n) + " *)"
	}
	for _, o := range []copyOp{src, dst} {
		if o.start < 0 {
			return "", refuse(m, "element %d of %s", o.start, o.array)
		}
	}

	// A single value (a structure, a scalar, one member): one assignment.
	// An array on the other side is its first element, as Logix reads it.
	if src.array == "" || dst.array == "" {
		if n != 1 {
			return "", refuse(m, "a length of %d from or into a single %s runs into the memory after it", n, dst.elem)
		}
		s, bad := rt.ref(src.one(), "")
		if bad != nil {
			return "", bad
		}
		d, bad := rt.ref(dst.one(), "")
		if bad != nil {
			return "", bad
		}
		rt.markWritten(in.Args[1])
		if src.array == "" && dst.array == "" && m == "COP" && rt.im.isUserType(dst.elem) {
			return "{ " + d + " := " + s + " }", nil // a whole structure: the writer's COP
		}
		return "{ " + d + " := " + s + pragma(1) + " }", nil
	}

	for _, o := range []copyOp{src, dst} {
		if o.start+n > o.count {
			return "", refuse(m, "a length of %d from element %d runs past the end of %s (%d elements)", n, o.start, o.array, o.count)
		}
	}
	if strings.EqualFold(src.array, dst.array) && src.start < dst.start+n && dst.start < src.start+n {
		return "", refuse(m, "source and destination overlap in %s; the result depends on the direction Logix copies in", dst.array)
	}

	if src.whole && dst.whole && n == src.count && n == dst.count {
		s, bad := rt.ref(in.Args[0], "")
		if bad != nil {
			return "", bad
		}
		d, bad := rt.ref(in.Args[1], "")
		if bad != nil {
			return "", bad
		}
		rt.markWritten(in.Args[1])
		if m == "CPS" {
			return "{ " + d + " := " + s + pragma(1) + " }", nil
		}
		return "{ " + d + " := " + s + " }", nil // a whole array: the writer's COP
	}

	if n > maxUnrolled {
		return "", refuse(m, "a slice of %d elements; nautilus spells a slice element by element, up to %d", n, maxUnrolled)
	}
	stmts := make([]string, 0, n)
	for k := 0; k < n; k++ {
		s, bad := rt.ref(src.array+"["+strconv.Itoa(src.start+k)+"]", "")
		if bad != nil {
			return "", bad
		}
		d, bad := rt.ref(dst.array+"["+strconv.Itoa(dst.start+k)+"]", "")
		if bad != nil {
			return "", bad
		}
		stmts = append(stmts, d+" := "+s)
	}
	rt.markWritten(in.Args[1])
	return "{ " + strings.Join(stmts, "; ") + pragma(n) + " }", nil
}

// one is the operand as a single value: an array's element at start.
func (o copyOp) one() string {
	if o.array == "" {
		return o.arg
	}
	return o.array + "[" + strconv.Itoa(o.start) + "]"
}

// copyOperand resolves a COP/CPS operand: a tag, a member path, or an
// element of a one-dimensional array at a constant index. A whole array
// starts at element 0, as Logix reads it.
func (rt *routine) copyOperand(m, a string) (copyOp, *refusal) {
	a = strings.TrimSpace(a)
	if a == "" || a == "?" {
		return copyOp{}, refuse("operand", "an unset operand")
	}
	if strings.Contains(a, ":") {
		return copyOp{}, refuse(m, "a copy of the module data %s", a)
	}
	base := baseOf(a)
	f := rt.sc.lookup(base)
	if f == nil {
		return copyOp{}, refuse("undefined", "tag %s is not in scope", base)
	}
	if f.alias != nil {
		return copyOp{}, f.alias
	}
	dtype, dims := f.dtype, f.dims
	path, rest := base, a[len(base):]
	for rest != "" {
		switch rest[0] {
		case '[':
			j := strings.IndexByte(rest, ']')
			if j < 0 {
				return copyOp{}, refuse("operand", "%s: an unclosed index", a)
			}
			inner := strings.TrimSpace(rest[1:j])
			if dims == "" {
				return copyOp{}, refuse(m, "%s indexes a value that is not an array", a)
			}
			if strings.Contains(inner, ",") || strings.Contains(strings.TrimSpace(dims), " ") {
				return copyOp{}, refuse(m, "a copy in a multi-dimensional array (%s)", a)
			}
			idx, err := strconv.Atoi(inner)
			if err != nil {
				return copyOp{}, refuse(m, "an element index that is not a constant (%s)", a)
			}
			count, err := strconv.Atoi(strings.TrimSpace(dims))
			if err != nil {
				return copyOp{}, refuse(m, "%s: array dimension %q", a, dims)
			}
			rest = rest[j+1:]
			if rest == "" {
				return copyOp{arg: a, array: path, start: idx, count: count, elem: dtype}, nil
			}
			path += "[" + inner + "]"
			dims = ""
		case '.':
			j := 1
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			name := rest[1:j]
			rest = rest[j:]
			if dims != "" {
				return copyOp{}, refuse(m, "%s: a member of a whole array", a)
			}
			if name == "" || strings.Trim(name, "0123456789") == "" {
				return copyOp{}, refuse(m, "a copy of a bit (%s)", a)
			}
			dt, dm, ok := rt.im.memberType(dtype, name)
			if !ok {
				return copyOp{}, refuse("type", "%s: %s has no member %s", a, dtype, name)
			}
			path += "." + name
			dtype, dims = dt, dm
		default:
			return copyOp{}, refuse("operand", "%s", a)
		}
	}
	if dims != "" {
		if strings.Contains(strings.TrimSpace(dims), " ") {
			return copyOp{}, refuse(m, "a copy of a multi-dimensional array (%s)", a)
		}
		count, err := strconv.Atoi(strings.TrimSpace(dims))
		if err != nil {
			return copyOp{}, refuse(m, "%s: array dimension %q", a, dims)
		}
		return copyOp{arg: a, array: path, start: 0, count: count, elem: dtype, whole: true}, nil
	}
	return copyOp{arg: a, elem: dtype, whole: true}, nil
}

// isUserType reports a structure the export defines (a UDT): the one kind
// of whole value the writer copies with COP on its own. Anything else (an
// elementary type, a STRING, which the writer would MOVE) keeps the
// pragma.
func (im *importer) isUserType(dtype string) bool {
	for _, dt := range im.file.Controller.DataTypes {
		if strings.EqualFold(dt.Name, dtype) {
			return true
		}
	}
	return false
}
