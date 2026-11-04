package ir

import "testing"

// A MemberWriter host is handed the assigned path root-first (#578); a
// path deeper than MaxMemberDepth is not a member path at all, and the VM
// falls back to a whole-global write.
func TestMemberPathIsRootFirst(t *testing.T) {
	// S.Items[2].X, as writeLValue builds it: leaf first.
	chain := []accessor{{fieldIdx: 0, isField: true}, {arrIdx: 1}, {fieldIdx: 4, isField: true}}
	p, ok := memberPath(chain)
	if !ok || p.Len() != 3 {
		t.Fatalf("memberPath = %+v, %v", p, ok)
	}
	for i, want := range []struct {
		idx   int
		field bool
	}{{4, true}, {1, false}, {0, true}} {
		if idx, field := p.Step(i); idx != want.idx || field != want.field {
			t.Errorf("step %d = (%d, %v), want (%d, %v)", i, idx, field, want.idx, want.field)
		}
	}
	if _, ok := memberPath(make([]accessor, MaxMemberDepth+1)); ok {
		t.Error("a path deeper than MaxMemberDepth was accepted")
	}
}

// memberHost is a stubHost that implements MemberWriter and keeps the
// path of the last member write.
type memberHost struct {
	*stubHost
	path   MemberPath
	member bool
}

func (h *memberHost) WriteGlobalMember(name string, v Value, p MemberPath) error {
	h.globals[name], h.path, h.member = v, p, true
	return nil
}

// A bit assignment reports only its bit to a MemberWriter (#578): on a plain
// global word (no steps), on a member, and for an IEC partial access and a
// bit of one, only the part's bits. Assigning the word itself reports it
// whole.
func TestBitWriteReportsItsBits(t *testing.T) {
	word := IntNamed("LWORD")
	rec := &StructDef{Name: "R", Fields: []StructField{{Name: "A", Type: IntT}, {Name: "W", Type: word}, {Name: "SI", Type: IntNamed("SINT")}}}
	recT := &Type{Kind: TypeStruct, Struct: rec}
	w := &GlobalRef{Name: "W", T: word}
	sw := &MemberRef{Object: &GlobalRef{Name: "S", T: recT}, FieldIdx: 1, T: word}
	byte1 := &BitRef{Object: w, Bit: 8, Width: 8, T: IntNamed("BYTE")}
	i := &GlobalRef{Name: "I", T: IntNamed("INT")}
	ibyte1 := &BitRef{Object: i, Bit: 8, Width: 8, T: IntNamed("BYTE"), Sign: 15}
	ssi := &MemberRef{Object: &GlobalRef{Name: "S", T: recT}, FieldIdx: 2, T: IntNamed("SINT")}
	for _, tc := range []struct {
		name   string
		target LValue
		value  Value
		steps  int
		bits   uint64
		member bool
	}{
		{"W.3", &BitRef{Object: w, Bit: 3}, BoolVal(true), 0, 1 << 3, true},
		{"W.63", &BitRef{Object: w, Bit: 63}, BoolVal(true), 0, 1 << 63, true},
		{"S.W.5", &BitRef{Object: sw, Bit: 5}, BoolVal(false), 1, 1 << 5, true},
		{"W.%B1", byte1, IntVal(0xFF), 0, 0xFF00, true},
		{"W.%B1.2", &BitRef{Object: byte1, Bit: 2}, BoolVal(true), 0, 1 << 10, true},
		{"W.%L0", &BitRef{Object: w, Bit: 0, Width: 64, T: word}, IntVal(-1), 0, ^uint64(0), true},
		{"S.W", sw, IntVal(1), 1, 0, true},
		// The sign bit of a signed word narrower than 64 bits carries the
		// bits above it (#611); a bit below it, or a part without it, does
		// not.
		{"I.15", &BitRef{Object: i, Bit: 15, Sign: 15}, BoolVal(true), 0, ^uint64(1<<15 - 1), true},
		{"I.15 cleared", &BitRef{Object: i, Bit: 15, Sign: 15}, BoolVal(false), 0, ^uint64(1<<15 - 1), true},
		{"I.14", &BitRef{Object: i, Bit: 14, Sign: 15}, BoolVal(true), 0, 1 << 14, true},
		{"I.%B1", ibyte1, IntVal(0x80), 0, ^uint64(1<<8 - 1), true},
		{"I.%B0", &BitRef{Object: i, Bit: 0, Width: 8, T: IntNamed("BYTE"), Sign: 15}, IntVal(0x80), 0, 0xFF, true},
		{"I.%B1.7", &BitRef{Object: ibyte1, Bit: 7}, BoolVal(true), 0, ^uint64(1<<15 - 1), true},
		{"I.%B1.6", &BitRef{Object: ibyte1, Bit: 6}, BoolVal(true), 0, 1 << 14, true},
		{"S.SI.7", &BitRef{Object: ssi, Bit: 7, Sign: 7}, BoolVal(true), 1, ^uint64(1<<7 - 1), true},
		{"W", w, IntVal(1), 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &memberHost{stubHost: newStubHost()}
			h.globals["W"] = Value{Kind: TypeInt}
			h.globals["I"] = Value{Kind: TypeInt}
			h.globals["S"] = Value{Kind: TypeStruct, Struct: rec, Fld: []Value{{Kind: TypeInt}, {Kind: TypeInt}, {Kind: TypeInt}}}
			prog := &Program{Body: []Stmt{&Assign{Target: tc.target, Value: &Lit{V: tc.value}}}}
			if err := Run(prog, NewFrame(prog), h); err != nil {
				t.Fatal(err)
			}
			if h.member != tc.member {
				t.Fatalf("member write = %v, want %v", h.member, tc.member)
			}
			if tc.member && (h.path.Len() != tc.steps || h.path.Bits() != tc.bits) {
				t.Errorf("path = %d steps, bits %#x; want %d steps, bits %#x", h.path.Len(), h.path.Bits(), tc.steps, tc.bits)
			}
		})
	}
}

type stubHost struct {
	globals map[string]Value
	now     int64
}

func newStubHost() *stubHost { return &stubHost{globals: map[string]Value{}} }

func (h *stubHost) ReadGlobal(name string) (Value, error)  { return h.globals[name], nil }
func (h *stubHost) WriteGlobal(name string, v Value) error { h.globals[name] = v; return nil }
func (h *stubHost) NowMs() int64                           { return h.now }

// Retained state: counter := counter + 1 across 5 scans lands at 5.
func TestRetainedAccumulator(t *testing.T) {
	prog := &Program{
		Name:  "test",
		Slots: []VarSlot{{Name: "counter", Type: IntT, Init: IntVal(0)}},
		Body: []Stmt{
			&Assign{
				Target: &SlotRef{Slot: 0, T: IntT},
				Value: &BinOp{
					Op: OpAdd,
					L:  &SlotRef{Slot: 0, T: IntT},
					R:  &Lit{V: IntVal(1), T: IntT},
					T:  IntT,
				},
			},
		},
	}
	frame := NewFrame(prog)
	host := newStubHost()
	for i := 0; i < 5; i++ {
		if err := Run(prog, frame, host); err != nil {
			t.Fatal(err)
		}
	}
	if got := frame.Slots[0].I; got != 5 {
		t.Errorf("counter = %d, want 5", got)
	}
}

func TestIfElse(t *testing.T) {
	prog := &Program{
		Slots: []VarSlot{
			{Name: "x", Type: IntT, Init: IntVal(10)},
			{Name: "result", Type: IntT},
		},
		Body: []Stmt{
			&If{
				Cond: &BinOp{
					Op: OpGt,
					L:  &SlotRef{Slot: 0, T: IntT},
					R:  &Lit{V: IntVal(5), T: IntT},
					T:  BoolT,
				},
				Then: []Stmt{&Assign{
					Target: &SlotRef{Slot: 1, T: IntT},
					Value:  &Lit{V: IntVal(1), T: IntT},
				}},
				Else: []Stmt{&Assign{
					Target: &SlotRef{Slot: 1, T: IntT},
					Value:  &Lit{V: IntVal(0), T: IntT},
				}},
			},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[1].I != 1 {
		t.Errorf("result = %d, want 1", frame.Slots[1].I)
	}
}

func TestForSum(t *testing.T) {
	prog := &Program{
		Slots: []VarSlot{
			{Name: "i", Type: IntT},
			{Name: "sum", Type: IntT},
		},
		Body: []Stmt{
			&For{
				Slot:  0,
				Start: &Lit{V: IntVal(1), T: IntT},
				End:   &Lit{V: IntVal(10), T: IntT},
				Body: []Stmt{&Assign{
					Target: &SlotRef{Slot: 1, T: IntT},
					Value: &BinOp{
						Op: OpAdd,
						L:  &SlotRef{Slot: 1, T: IntT},
						R:  &SlotRef{Slot: 0, T: IntT},
						T:  IntT,
					},
				}},
			},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[1].I != 55 {
		t.Errorf("sum = %d, want 55", frame.Slots[1].I)
	}
}

func TestWhileCountdown(t *testing.T) {
	prog := &Program{
		Slots: []VarSlot{
			{Name: "n", Type: IntT, Init: IntVal(5)},
			{Name: "iters", Type: IntT},
		},
		Body: []Stmt{
			&While{
				Cond: &BinOp{
					Op: OpGt,
					L:  &SlotRef{Slot: 0, T: IntT},
					R:  &Lit{V: IntVal(0), T: IntT},
					T:  BoolT,
				},
				Body: []Stmt{
					&Assign{
						Target: &SlotRef{Slot: 0, T: IntT},
						Value: &BinOp{
							Op: OpSub,
							L:  &SlotRef{Slot: 0, T: IntT},
							R:  &Lit{V: IntVal(1), T: IntT},
							T:  IntT,
						},
					},
					&Assign{
						Target: &SlotRef{Slot: 1, T: IntT},
						Value: &BinOp{
							Op: OpAdd,
							L:  &SlotRef{Slot: 1, T: IntT},
							R:  &Lit{V: IntVal(1), T: IntT},
							T:  IntT,
						},
					},
				},
			},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[0].I != 0 || frame.Slots[1].I != 5 {
		t.Errorf("n=%d iters=%d, want 0,5", frame.Slots[0].I, frame.Slots[1].I)
	}
}

func TestCaseMatch(t *testing.T) {
	// CASE x OF 1: result := 10; 2,3: result := 20; ELSE result := 99
	prog := &Program{
		Slots: []VarSlot{
			{Name: "x", Type: IntT, Init: IntVal(3)},
			{Name: "result", Type: IntT},
		},
		Body: []Stmt{
			&Case{
				Expr: &SlotRef{Slot: 0, T: IntT},
				Clauses: []CaseClause{
					{Values: []Expr{&Lit{V: IntVal(1), T: IntT}}, Body: []Stmt{
						&Assign{Target: &SlotRef{Slot: 1, T: IntT}, Value: &Lit{V: IntVal(10), T: IntT}},
					}},
					{Values: []Expr{&Lit{V: IntVal(2), T: IntT}, &Lit{V: IntVal(3), T: IntT}}, Body: []Stmt{
						&Assign{Target: &SlotRef{Slot: 1, T: IntT}, Value: &Lit{V: IntVal(20), T: IntT}},
					}},
				},
				Else: []Stmt{
					&Assign{Target: &SlotRef{Slot: 1, T: IntT}, Value: &Lit{V: IntVal(99), T: IntT}},
				},
			},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[1].I != 20 {
		t.Errorf("result = %d, want 20", frame.Slots[1].I)
	}
}

func TestRealArithmetic(t *testing.T) {
	// x := 1.5 + 2.25
	prog := &Program{
		Slots: []VarSlot{{Name: "x", Type: RealT}},
		Body: []Stmt{
			&Assign{
				Target: &SlotRef{Slot: 0, T: RealT},
				Value: &BinOp{
					Op: OpAdd,
					L:  &Lit{V: RealVal(1.5), T: RealT},
					R:  &Lit{V: RealVal(2.25), T: RealT},
					T:  RealT,
				},
			},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[0].F != 3.75 {
		t.Errorf("x = %v, want 3.75", frame.Slots[0].F)
	}
}

func TestGlobalReadWrite(t *testing.T) {
	// motor_run := enabled AND NOT fault
	prog := &Program{
		Body: []Stmt{
			&Assign{
				Target: &GlobalRef{Name: "motor_run", T: BoolT},
				Value: &BinOp{
					Op: OpAnd,
					L:  &GlobalRef{Name: "enabled", T: BoolT},
					R:  &UnOp{Op: OpNot, X: &GlobalRef{Name: "fault", T: BoolT}, T: BoolT},
					T:  BoolT,
				},
			},
		},
	}
	frame := NewFrame(prog)
	host := newStubHost()
	host.globals["enabled"] = BoolVal(true)
	host.globals["fault"] = BoolVal(false)
	if err := Run(prog, frame, host); err != nil {
		t.Fatal(err)
	}
	if !host.globals["motor_run"].B {
		t.Errorf("motor_run = %v, want true", host.globals["motor_run"].B)
	}
}

func TestReturnEarly(t *testing.T) {
	// if x > 0 then result := 1; return; end_if; result := -1;
	prog := &Program{
		Slots: []VarSlot{
			{Name: "x", Type: IntT, Init: IntVal(5)},
			{Name: "result", Type: IntT},
		},
		Body: []Stmt{
			&If{
				Cond: &BinOp{
					Op: OpGt,
					L:  &SlotRef{Slot: 0, T: IntT},
					R:  &Lit{V: IntVal(0), T: IntT},
					T:  BoolT,
				},
				Then: []Stmt{
					&Assign{Target: &SlotRef{Slot: 1, T: IntT}, Value: &Lit{V: IntVal(1), T: IntT}},
					&Return{},
				},
			},
			&Assign{Target: &SlotRef{Slot: 1, T: IntT}, Value: &Lit{V: IntVal(-1), T: IntT}},
		},
	}
	frame := NewFrame(prog)
	if err := Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if frame.Slots[1].I != 1 {
		t.Errorf("result = %d, want 1 (RETURN should have skipped the trailing assign)", frame.Slots[1].I)
	}
}

// Type.SignBit names the sign bit of a signed integer narrower than 64 bits
// and nothing else (#611).
func TestSignBit(t *testing.T) {
	for name, want := range map[string]int{
		"SINT": 7, "INT": 15, "DINT": 31, "LINT": 0,
		"USINT": 0, "UINT": 0, "UDINT": 0, "ULINT": 0,
		"BYTE": 0, "WORD": 0, "DWORD": 0, "LWORD": 0,
	} {
		if got := IntNamed(name).SignBit(); got != want {
			t.Errorf("%s.SignBit() = %d, want %d", name, got, want)
		}
	}
	for _, typ := range []*Type{IntT, RealT, BoolT, nil, {Kind: TypeInt, Name: "INT", Enum: &EnumDef{}}} {
		if got := typ.SignBit(); got != 0 {
			t.Errorf("%v.SignBit() = %d, want 0", typ, got)
		}
	}
}
