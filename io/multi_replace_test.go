package io

import (
	"strings"
	"sync"
	"testing"
)

// #706: Replace swaps one child of a running set; reads, writes and quality
// follow the new child, and the other children are untouched.
func TestMultiReplaceRoutesToTheNewChild(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A_In"}, outs: []string{"A_Out"}}
	b := named{Memory: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	a.WriteOutputs(Values{"A_In": 1.0})
	b.WriteOutputs(Values{"B_In": 2.0})
	if err := m.ReadInputsInto(Values{}); err != nil { // fill b's scratch map
		t.Fatal(err)
	}

	b2 := named{Memory: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}
	b2.WriteOutputs(Values{"B_In": 20.0})
	b2.SetQuality("B_In", Bad)
	old, err := m.Replace("b", b2)
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := old.(named); !ok || o.Memory != b.Memory {
		t.Errorf("Replace returned %T, want the replaced child", old)
	}
	got := Values{}
	if err := m.ReadInputsInto(got); err != nil {
		t.Fatal(err)
	}
	if got["B_In"] != 20.0 || got["A_In"] != 1.0 {
		t.Errorf("read after Replace = %v, want B_In from the new child and A_In as before", got)
	}
	if err := m.WriteOutputs(Values{"B_Out": 5.0, "A_Out": 6.0}); err != nil {
		t.Fatal(err)
	}
	if v, _ := b2.ReadInputs(); v["B_Out"] != 5.0 {
		t.Errorf("the new child did not get B_Out: %v", v)
	}
	if v, _ := b.ReadInputs(); v["B_Out"] != nil {
		t.Errorf("the replaced child still gets writes: %v", v)
	}
	if q := m.Quality(); q["B_In"] != Bad {
		t.Errorf("Quality after Replace = %v, want the new child's", q)
	}
	if d, _ := m.Child("b"); d.(named).Memory != b2.Memory {
		t.Errorf("Child(b) = %T, want the replacement", d)
	}
}

func TestMultiReplaceRefuses(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A_In"}, outs: []string{"A_Out"}}
	b := named{Memory: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, child string
		d           Driver
		want        string
	}{
		{"unknown child", "c", named{Memory: NewMemory()}, "no driver named"},
		{"nil", "b", nil, "nil"},
		{"no tag ownership", "b", NewMemory(), "does not declare"},
		{"an input another child delivers", "b", named{Memory: NewMemory(), ins: []string{"A_In"}}, `"A_In"`},
		{"an output another child accepts", "b", named{Memory: NewMemory(), outs: []string{"A_Out"}}, `"A_Out"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := m.Replace(c.child, c.d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Replace: %v, want an error mentioning %s", err, c.want)
			}
			if d, _ := m.Child("b"); d.(named).Memory != b.Memory {
				t.Error("a refused Replace changed the set")
			}
		})
	}
}

// The write gates installed on the set so far (the leadership gate, the
// live-scans plain gate) reach a child brought in by Replace: a standby's
// new Modbus driver must not drive the device either.
func TestMultiReplaceKeepsTheWriteGates(t *testing.T) {
	var gA, gB, gB2 func() bool
	var sent []string
	a := gated{named: named{Memory: NewMemory(), outs: []string{"A_Out"}}, gate: &gA, sent: &sent}
	b := gated{named: named{Memory: NewMemory(), outs: []string{"B_Out"}}, gate: &gB, sent: &sent}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	closed := func() bool { return false }
	m.SetWriteGate(closed)
	b2 := gated{named: named{Memory: NewMemory(), outs: []string{"B_Out"}}, gate: &gB2, sent: &sent}
	if _, err := m.Replace("b", b2); err != nil {
		t.Fatal(err)
	}
	if gB2 == nil {
		t.Fatal("the replacement has no write gate")
	}
	m.WriteOutputs(Values{"B_Out": 1.0})
	if len(sent) != 0 {
		t.Errorf("a closed gate let %v out through the replacement", sent)
	}

	// A set that only ever had a plain gate (the live-scans gate, no
	// redundancy): the replacement gets that one.
	var c0, pg func() bool
	m2, err := NewMulti(NamedDriver{"c", plainGated{gated{named: named{Memory: NewMemory(), outs: []string{"C_Out"}}, gate: &c0, sent: &sent}}})
	if err != nil {
		t.Fatal(err)
	}
	SetPlainGate(m2, closed)
	p := plainGated{gated{named: named{Memory: NewMemory(), outs: []string{"C_Out"}}, gate: &pg, sent: &sent}}
	if _, err := m2.Replace("c", p); err != nil {
		t.Fatal(err)
	}
	if pg == nil {
		t.Error("a plain gate installed on the set did not reach the replacement")
	}
}

// Routing follows the replacement's own tags: a write to a tag only the
// new child accepts reaches it, one only the old child accepted is dropped.
func TestMultiReplaceReroutes(t *testing.T) {
	a := named{Memory: NewMemory(), outs: []string{"A_Out"}}
	b := named{Memory: NewMemory(), outs: []string{"B_Out"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	b2 := named{Memory: NewMemory(), outs: []string{"B_Out2"}}
	if _, err := m.Replace("b", b2); err != nil {
		t.Fatal(err)
	}
	m.WriteOutputs(Values{"B_Out": 1.0, "B_Out2": 2.0})
	if v, _ := b2.ReadInputs(); v["B_Out2"] != 2.0 || v["B_Out"] != nil {
		t.Errorf("the replacement got %v, want only B_Out2", v)
	}
	if got := m.OutputNames(); len(got) != 2 || got[0] != "A_Out" || got[1] != "B_Out2" {
		t.Errorf("OutputNames after Replace = %v", got)
	}
}

// askingChild asks for the outputs (OutputsRequester).
type askingChild struct {
	named
	want *bool
}

func (a askingChild) OutputsWanted() bool { w := *a.want; *a.want = false; return w }

// The set asks the children that can ask: after Replace, the new child's
// asking counts and the old one's does not.
func TestMultiReplaceRefreshesTheAskers(t *testing.T) {
	want := true
	a := named{Memory: NewMemory(), ins: []string{"A_In"}}
	b := askingChild{named: named{Memory: NewMemory(), ins: []string{"B_In"}}, want: &want}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Replace("b", named{Memory: NewMemory(), ins: []string{"B_In"}}); err != nil {
		t.Fatal(err)
	}
	if m.OutputsWanted() {
		t.Error("the replaced child's request still counts")
	}
	want2 := true
	if _, err := m.Replace("b", askingChild{named: named{Memory: NewMemory(), ins: []string{"B_In"}}, want: &want2}); err != nil {
		t.Fatal(err)
	}
	if !m.OutputsWanted() {
		t.Error("the new child's request is not asked")
	}
}

// Reads, writes and quality go on while children are replaced (run with
// -race: the set lock is what makes this safe).
func TestMultiReplaceUnderConcurrentUse(t *testing.T) {
	a := named{Memory: NewMemory(), ins: []string{"A_In"}, outs: []string{"A_Out"}}
	b := named{Memory: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}
	m, err := NewMulti(NamedDriver{"a", a}, NamedDriver{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := Values{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.ReadInputsInto(dst)
				_ = m.WriteOutputs(Values{"A_Out": 1.0, "B_Out": 2.0})
				_ = m.Quality()
				_ = m.OutputsWanted()
				_ = m.InputsReady()
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if _, err := m.Replace("b", named{Memory: NewMemory(), ins: []string{"B_In"}, outs: []string{"B_Out"}}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}
