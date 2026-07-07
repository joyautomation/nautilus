
package ir

import "testing"

type stubHost struct {
	globals map[string]Value
	now     int64
}

func newStubHost() *stubHost { return &stubHost{globals: map[string]Value{}} }

func (h *stubHost) ReadGlobal(name string) (Value, error)   { return h.globals[name], nil }
func (h *stubHost) WriteGlobal(name string, v Value) error  { h.globals[name] = v; return nil }
func (h *stubHost) NowMs() int64                            { return h.now }

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
