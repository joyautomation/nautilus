package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/st"
)

// The diagram editor's copy/cut/paste/delete ops against the scaffolded
// Demo program.fbd, for every selection shape a user can make: each node
// alone, each coil with its inline blocks, select-all, and random subsets.
// A real-VS-Code smoke test found select-all → paste writing MUL(_, _0.0):
// one statement reached through two selected ids was cut twice.
//
// The contract (lang/fbd/editparity.go): an edit may leave semantic holes
// (`_` open pins, undeclared *_copy targets) for diagnostics to guide, but
// its result must always PARSE — and a cut + paste of the whole diagram
// must round-trip to a program that checks clean.
func TestDiagramPasteProperty(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	sc := scaffold{Name: "demo", Program: "Demo", Template: tmplDemo}
	err := write(&sc)
	os.Chdir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, "demo", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	src, blocks := read("program.fbd"), read("blocks.st")
	if msg, bad := checkFBD(src, blocks); bad {
		t.Fatalf("scaffolded program.fbd doesn't check: %s", msg)
	}
	m, err := fbd.Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range m.Nodes {
		ids = append(ids, n.ID)
	}

	var sels [][]string
	for _, id := range ids {
		sels = append(sels, []string{id})
		if strings.HasPrefix(id, "c:") {
			// The coil plus its inline blocks — a rubber-band over one rung.
			pair := []string{id}
			for _, o := range ids {
				if strings.HasPrefix(o, "b:c."+strings.TrimPrefix(id, "c:")) {
					pair = append(pair, o)
				}
			}
			sels = append(sels, pair)
		}
	}
	sels = append(sels, ids)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 150; i++ {
		var sub []string
		for _, id := range ids {
			if rng.Intn(2) == 0 {
				sub = append(sub, id)
			}
		}
		rng.Shuffle(len(sub), func(a, b int) { sub[a], sub[b] = sub[b], sub[a] })
		sels = append(sels, sub)
	}

	other := "PROGRAM Other\nVAR\n  x : BOOL;\nEND_VAR\nFBD\n  x := TRUE\nEND_FBD\nEND_PROGRAM\n"
	for _, sel := range sels {
		// In place, and into another file / this file from a snapshot.
		for _, c := range []struct {
			name, target string
			op           fbd.EditOp
		}{
			{"duplicate", src, fbd.EditOp{Type: "duplicate", Nodes: sel}},
			{"paste-snapshot", src, fbd.EditOp{Type: "duplicate", Nodes: sel, Text: src}},
			{"paste-other", other, fbd.EditOp{Type: "duplicate", Nodes: sel, Text: src}},
			{"paste-other-keeprefs", other, fbd.EditOp{Type: "duplicate", Nodes: sel, Text: src, KeepRefs: true}},
		} {
			edits, err := fbd.ApplyEdit(c.target, c.op)
			if err != nil {
				if strings.Contains(err.Error(), "nothing copyable") {
					continue
				}
				t.Fatalf("%s %v: %v", c.name, sel, err)
			}
			out := applyFBDEdits(c.target, edits)
			if msg, bad := parseFBD(out); bad {
				t.Fatalf("%s %v writes source that no longer parses: %s\n%s", c.name, sel, msg, out)
			}
		}

		// Cut: the batched delete, then a keepRefs paste of the snapshot.
		edits, err := fbd.ApplyEdit(src, fbd.EditOp{Type: "deleteNode", Nodes: sel})
		if err != nil {
			if onlyRideAlong(sel) {
				continue // a lone chip/inline block is not deletable
			}
			t.Fatalf("deleteNode %v: %v", sel, err)
		}
		cut := applyFBDEdits(src, edits)
		if msg, bad := parseFBD(cut); bad {
			t.Fatalf("deleteNode %v writes source that no longer parses: %s\n%s", sel, msg, cut)
		}
		edits, err = fbd.ApplyEdit(cut, fbd.EditOp{Type: "duplicate", Nodes: sel, Text: src, KeepRefs: true})
		if err != nil {
			if strings.Contains(err.Error(), "nothing copyable") {
				continue
			}
			t.Fatalf("cut+paste %v: %v", sel, err)
		}
		pasted := applyFBDEdits(cut, edits)
		if msg, bad := parseFBD(pasted); bad {
			t.Fatalf("cut+paste %v writes source that no longer parses: %s\n%s", sel, msg, pasted)
		}
		if len(sel) == len(ids) {
			if msg, bad := checkFBD(pasted, blocks); bad {
				t.Fatalf("select-all cut+paste doesn't check clean: %s\n%s", msg, pasted)
			}
		}
	}

	// Select-all copy → paste in place: every leaf outside the selection
	// severs to exactly `_` — no garbled splices.
	edits, err := fbd.ApplyEdit(src, fbd.EditOp{Type: "duplicate", Nodes: ids})
	if err != nil {
		t.Fatal(err)
	}
	out := applyFBDEdits(src, edits)
	for _, want := range []string{
		"integral_copy := LIMIT(0.0, ADD(integral_copy, MUL(_, e_copy, _)), 100.0)",
		"Heater_copy := LIMIT(0.0, ADD(MUL(_, e_copy), integral_copy), 100.0)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("select-all paste lacks %q:\n%s", want, out)
		}
	}
}

func onlyRideAlong(sel []string) bool {
	for _, id := range sel {
		if !strings.HasPrefix(id, "v:") && !strings.HasPrefix(id, "k:") && !strings.HasPrefix(id, "b:c.") {
			return false
		}
	}
	return true
}

// parseFBD is the parse half of naut check: the netlist graphs, transpiles,
// and the ST parses.
func parseFBD(src string) (string, bool) {
	if _, err := fbd.Graph(src); err != nil {
		return err.Error(), true
	}
	stSrc, err := fbd.Transpile(src)
	if err != nil {
		return err.Error(), true
	}
	if _, err := st.Parse(stSrc); err != nil {
		return err.Error(), true
	}
	return "", false
}

// checkFBD is naut check's compile for an .fbd with blocks.st in scope.
func checkFBD(src, blocks string) (string, bool) {
	stSrc, err := fbd.Transpile(src)
	if err != nil {
		return err.Error(), true
	}
	msg, _, bad := compileErr(stSrc, blocks, strings.Count(blocks, "\n"))
	return msg, bad
}

// applyFBDEdits applies 1-based end-exclusive text edits bottom-up.
func applyFBDEdits(src string, edits []fbd.TextEdit) string {
	lines := strings.Split(src, "\n")
	sorted := append([]fbd.TextEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Line != sorted[j].Line {
			return sorted[i].Line > sorted[j].Line
		}
		return sorted[i].Col > sorted[j].Col
	})
	for _, e := range sorted {
		endIdx := e.EndLine - 1
		var tail string
		if endIdx < len(lines) {
			tail = lines[endIdx][e.EndCol-1:]
		}
		head := lines[e.Line-1][:e.Col-1]
		repl := strings.Split(head+e.NewText+tail, "\n")
		rest := []string{}
		if endIdx+1 < len(lines) {
			rest = append(rest, lines[endIdx+1:]...)
		}
		lines = append(append(lines[:e.Line-1:e.Line-1], repl...), rest...)
	}
	return strings.Join(lines, "\n")
}
