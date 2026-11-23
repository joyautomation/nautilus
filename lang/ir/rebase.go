package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// TimeBaseSlot is one FB internal slot holding an absolute time on the
// host's clock (FBDef.TimeBase). Clear says a hot takeover sets it to 0
// (idle) instead of shifting it: the PID's last call, whose shift would
// integrate across the takeover.
type TimeBaseSlot struct {
	Index int
	Clear bool
}

// RebaseFrame moves every function-block instance in f, at any depth
// (FB in FB, arrays and structs of FBs), from one clock to another d
// milliseconds ahead of it: each non-zero TimeBase slot gains d, or is set
// to 0 when it is a Clear slot. A zero slot is idle and stays 0. A pair's
// hot takeover runs it on the frames it carries (#710 S2): d is the new
// leader's clock at the image's arrival minus the image's NowMs, so a
// timer counts the image's age as elapsed, as design §4.2 step 4 says.
func RebaseFrame(f *Frame, d int64) {
	if f == nil {
		return
	}
	for i := range f.Slots {
		rebaseValue(&f.Slots[i], d)
	}
}

func rebaseValue(v *Value, d int64) {
	switch v.Kind {
	case TypeArray:
		for i := range v.Arr {
			rebaseValue(&v.Arr[i], d)
		}
	case TypeStruct:
		for i := range v.Fld {
			rebaseValue(&v.Fld[i], d)
		}
	case TypeFB:
		if v.FB == nil {
			return
		}
		if v.FB.Def != nil {
			for _, tb := range v.FB.Def.TimeBase {
				if tb.Index < 0 || tb.Index >= len(v.FB.Slots) {
					continue
				}
				s := &v.FB.Slots[tb.Index]
				switch {
				case s.I == 0:
				case tb.Clear:
					s.I = 0
				default:
					s.I += d
				}
			}
		}
		for i := range v.FB.Slots {
			rebaseValue(&v.FB.Slots[i], d)
		}
	}
}

// ForgetHost zeroes every HostLocal slot of every function-block
// instance in f, at any depth: a frame carried to another controller
// (#710 S2 hot takeover) holds no handle into the new host's tables. A
// READ or WRITE that was BUSY then polls ticket 0 and reports its request
// lost (ERROR, STATUS lost), never another request's reply.
func ForgetHost(f *Frame) {
	if f == nil {
		return
	}
	for i := range f.Slots {
		forgetValue(&f.Slots[i])
	}
}

func forgetValue(v *Value) {
	switch v.Kind {
	case TypeArray:
		for i := range v.Arr {
			forgetValue(&v.Arr[i])
		}
	case TypeStruct:
		for i := range v.Fld {
			forgetValue(&v.Fld[i])
		}
	case TypeFB:
		if v.FB == nil {
			return
		}
		if v.FB.Def != nil {
			for _, i := range v.FB.Def.HostLocal {
				if i >= 0 && i < len(v.FB.Slots) {
					v.FB.Slots[i].I = 0
				}
			}
		}
		for i := range v.FB.Slots {
			forgetValue(&v.FB.Slots[i])
		}
	}
}

// FlattenValueInto appends v's leaves to dst, in FlattenInto's order, and
// returns it: one tag's value for a state image (#710 S2). Like
// FlattenInto it copies values only.
func FlattenValueInto(dst []Leaf, v *Value) []Leaf { return flattenValue(dst, v) }

// ApplyValueLeaves writes leaves, as FlattenValueInto wrote them, into v,
// which must have the shape they were taken from (Zero of the tag's type):
// every leaf of the kind v holds at its place, and every leaf used, else
// ErrLeaves. v may be partly written on an error.
func ApplyValueLeaves(v *Value, leaves []Leaf) error {
	n, err := applyValue(v, leaves, 0)
	if err != nil {
		return err
	}
	if n != len(leaves) {
		return fmt.Errorf("%w: %d leaves, the value holds %d", ErrLeaves, len(leaves), n)
	}
	return nil
}

// TagsLayout identifies the shape of a set of tags as a state image
// carries them: each name, in the order given, with its declared type
// walked as Layout walks a slot's, plus FrameFormat. Two controllers
// whose carried tags have one TagsLayout read each other's tag leaves,
// tag for tag. nil types are allowed (an untyped tag) and hash as such.
func TagsLayout(names []string, types []*Type) string {
	h := sha256.New()
	w := layoutWriter{h: h}
	w.int(FrameFormat)
	w.int(int64(len(names)))
	for i, n := range names {
		w.str(n)
		var t *Type
		if i < len(types) {
			t = types[i]
		}
		w.typ(t, 0)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}
