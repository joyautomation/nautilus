package ld

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
)

type strHost struct{ vals map[string]ir.Value }

func (h *strHost) ReadGlobal(n string) (ir.Value, error)  { return h.vals[n], nil }
func (h *strHost) WriteGlobal(n string, v ir.Value) error { h.vals[n] = v; return nil }
func (h *strHost) NowMs() int64                           { return 0 }

// A string in a rung reads as in ST (nautilus#591): '…' or "…", and $' or
// $" is a quote inside it, not its end — in an assignment box, where a ';'
// inside the string must not split the statement, and in a block's
// arguments. The value reaches the tag.
func TestRungStringsReadAsInST(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Msg : STRING; Note : STRING; Same : BOOL; END_VAR
LD
  RUNG text
    Go { Msg := 'it$'s; ok'; Note := "say $"hi$" (* not a comment *)" }
  RUNG compare
    EQ(Msg, 'it$'s; ok') ( Same )
END_LD
END_PROGRAM
`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := fbd.Compile(out)
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	h := &strHost{vals: map[string]ir.Value{"Go": ir.BoolVal(true), "Msg": ir.StringVal(""), "Note": ir.StringVal("")}}
	frame := ir.NewFrame(prog)
	for i := 0; i < 2; i++ {
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.vals["Msg"].S; got != "it's; ok" {
		t.Errorf("Msg = %q, want %q", got, "it's; ok")
	}
	if got := h.vals["Note"].S; got != `say "hi" (* not a comment *)` {
		t.Errorf("Note = %q", got)
	}
	if !h.vals["Same"].B {
		t.Errorf("Same = %+v, want TRUE (EQ read the same string)", h.vals["Same"])
	}
}
