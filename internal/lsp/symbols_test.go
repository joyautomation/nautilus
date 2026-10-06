package lsp

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outlineText renders a symbol tree one node per line, indented by depth:
// "name [kind] detail". Positions are checked separately.
func outlineText(syms []DocumentSymbol) string {
	var b strings.Builder
	var walk func([]DocumentSymbol, int)
	walk = func(ss []DocumentSymbol, depth int) {
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s [%d]", strings.Repeat("  ", depth), s.Name, s.Kind)
			if s.Detail != "" {
				fmt.Fprintf(&b, " %s", s.Detail)
			}
			b.WriteByte('\n')
			walk(s.Children, depth+1)
		}
	}
	walk(syms, 0)
	return b.String()
}

// findSym returns the first symbol with this name, depth first.
func findSym(syms []DocumentSymbol, name string) *DocumentSymbol {
	for i := range syms {
		if syms[i].Name == name {
			return &syms[i]
		}
		if s := findSym(syms[i].Children, name); s != nil {
			return s
		}
	}
	return nil
}

func wantOutline(t *testing.T, got []DocumentSymbol, want string) {
	t.Helper()
	want = strings.TrimLeft(want, "\n")
	if g := outlineText(got); g != want {
		t.Errorf("outline:\n%s\nwant:\n%s", g, want)
	}
}

// wantRange checks a symbol's range and selection range, as 1-based
// "line:col-line:col" (col 1-based too, end exclusive) — the user's file.
func wantRange(t *testing.T, syms []DocumentSymbol, name, rng, sel string) {
	t.Helper()
	s := findSym(syms, name)
	if s == nil {
		t.Errorf("no symbol %q", name)
		return
	}
	f := func(r Range) string {
		return fmt.Sprintf("%d:%d-%d:%d", r.Start.Line+1, r.Start.Character+1, r.End.Line+1, r.End.Character+1)
	}
	if got := f(s.Range); got != rng {
		t.Errorf("%s range = %s, want %s", name, got, rng)
	}
	if got := f(s.SelectionRange); got != sel {
		t.Errorf("%s selectionRange = %s, want %s", name, got, sel)
	}
}

const symST = `TYPE
  Recipe : STRUCT
    Name   : STRING;
    Amount : REAL; (* liters *)
  END_STRUCT;
  Liters : REAL;
  Mode : (Off, Hand, Auto);
END_TYPE

FUNCTION_BLOCK Valve
VAR_INPUT
    Open : BOOL;
END_VAR
VAR_OUTPUT
    Opened, Closed : BOOL;
END_VAR
Opened := Open;
Closed := NOT Open;
END_FUNCTION_BLOCK

FUNCTION Clamp : REAL
VAR_INPUT
    X : REAL;
END_VAR
Clamp := LIMIT(0.0, X, 100.0);
END_FUNCTION

PROGRAM Plant
VAR_EXTERNAL
    Level : REAL;
END_VAR
VAR CONSTANT
    MaxLevel : REAL := 95.0;
END_VAR
VAR
    v1      : Valve;
    Batches : ARRAY[1..3] OF Recipe;
END_VAR
VAR_TEMP
    i : INT;
END_VAR
v1(Open := Level < MaxLevel);
END_PROGRAM
`

func TestDocumentSymbolsST(t *testing.T) {
	syms := documentSymbols("file:///plant.st", symST)
	wantOutline(t, syms, `
Recipe [23] STRUCT
  Name [8] STRING
  Amount [8] REAL
Liters [26] REAL
Mode [10] (Off, Hand, Auto)
  Off [22]
  Hand [22]
  Auto [22]
Valve [5] FUNCTION_BLOCK
  VAR_INPUT [3]
    Open [13] BOOL
  VAR_OUTPUT [3]
    Opened [13] BOOL
    Closed [13] BOOL
Clamp [12] FUNCTION : REAL
  VAR_INPUT [3]
    X [13] REAL
Plant [2] PROGRAM
  VAR_EXTERNAL [3]
    Level [13] REAL
  VAR [3] CONSTANT
    MaxLevel [14] REAL
  VAR [3]
    v1 [13] Valve
    Batches [13] ARRAY[1..3] OF Recipe
  VAR_TEMP [3]
    i [13] INT
`)
	// The POU spans keyword → END_ keyword, the name is the selection.
	wantRange(t, syms, "Plant", "28:1-43:12", "28:9-28:14")
	wantRange(t, syms, "Valve", "10:1-19:19", "10:16-10:21")
	// The first TYPE starts at the TYPE keyword, the last ends at END_TYPE.
	wantRange(t, syms, "Recipe", "1:1-5:14", "2:3-2:9")
	wantRange(t, syms, "Mode", "7:3-8:9", "7:3-7:7")
	// A section spans VAR … END_VAR; a declaration its name through ';'.
	wantRange(t, syms, "VAR_EXTERNAL", "29:1-31:8", "29:1-29:13")
	wantRange(t, syms, "MaxLevel", "33:5-33:29", "33:5-33:13")
	// `Opened, Closed : BOOL;` — each name selects itself.
	wantRange(t, syms, "Closed", "15:5-15:27", "15:13-15:19")
}

const symFBD = `FUNCTION_BLOCK Latch
VAR_INPUT
    Set, Reset : BOOL;
END_VAR
VAR_OUTPUT
    Q : BOOL;
END_VAR
FBD
  Q := AND(OR(Set, Q), NOT Reset)
END_FBD
END_FUNCTION_BLOCK

PROGRAM Main
VAR
    Run   : BOOL;
    Start : BOOL;
    Delay : BOOL;
    l1    : Latch;
END_VAR
FBD
  // a seal-in, then a delay
  latch = OR(Start, Run)
  t1 : TON(IN := latch,
           PT := T#5s)
  Run := latch
  l1(Set := Start, Reset := t1.Q)
  Delay := t1.Q;
END_FBD
END_PROGRAM
`

func TestDocumentSymbolsFBD(t *testing.T) {
	syms := documentSymbols("file:///main.fbd", symFBD)
	wantOutline(t, syms, `
Latch [5] FUNCTION_BLOCK
  VAR_INPUT [3]
    Set [13] BOOL
    Reset [13] BOOL
  VAR_OUTPUT [3]
    Q [13] BOOL
  Q [7] := AND(OR(Set, Q), NOT Reset)
Main [2] PROGRAM
  VAR [3]
    Run [13] BOOL
    Start [13] BOOL
    Delay [13] BOOL
    l1 [13] Latch
  latch [13] = OR(Start, Run)
  t1 [19] TON
  Run [7] := latch
  l1 [6] (Set := Start, Reset := t1.Q)
  Delay [7] := t1.Q;
`)
	// Netlist statements are positioned in the .fbd itself: t1 spans its
	// two lines; the name is the selection.
	main := findSym(syms, "Main")
	t1 := findSym(main.Children, "t1")
	if got := fmt.Sprintf("%d:%d-%d:%d", t1.Range.Start.Line+1, t1.Range.Start.Character+1, t1.Range.End.Line+1, t1.Range.End.Character+1); got != "23:3-24:23" {
		t.Errorf("t1 range = %s, want 23:3-24:23", got)
	}
	if t1.SelectionRange.Start != (Position{Line: 22, Character: 2}) || t1.SelectionRange.End != (Position{Line: 22, Character: 4}) {
		t.Errorf("t1 selection = %+v", t1.SelectionRange)
	}
}

const symLD = `PROGRAM Interlocks
VAR_EXTERNAL
    Start, Stop : BOOL;
    Motor       : BOOL;
END_VAR
VAR
    t1 : TON;
END_VAR
LD
  // seal-in
  RUNG run (* start/stop with seal-in *)
    [ Start | Motor ] /Stop ( Motor )

  RUNG
    Motor t1:TON(PT := T#5s)

  RUNG multi (* the comment
                runs on *)
    Motor ( S Stop )
END_LD
END_PROGRAM
`

func TestDocumentSymbolsLD(t *testing.T) {
	syms := documentSymbols("file:///interlocks.ld", symLD)
	wantOutline(t, syms, `
Interlocks [2] PROGRAM
  VAR_EXTERNAL [3]
    Start [13] BOOL
    Stop [13] BOOL
    Motor [13] BOOL
  VAR [3]
    t1 [13] TON
  run [24] start/stop with seal-in
  rung14 [24]
  multi [24] the comment runs on
`)
	wantRange(t, syms, "run", "11:3-12:38", "11:8-11:11")
	// An unnamed rung selects its RUNG keyword.
	wantRange(t, syms, "rung14", "14:3-15:29", "14:3-14:7")
	wantRange(t, syms, "multi", "17:3-19:21", "17:8-17:13")
}

const symSFC = `PROGRAM Filler
VAR_EXTERNAL
    Level : REAL;
    Pump  : BOOL;
END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP

  STEP Fill:
    N Pump;
    P1 Count;
  END_STEP

  STEP Hold:
  END_STEP

  TRANSITION t_fill FROM Idle TO Fill := Level < 10.0;
  END_TRANSITION
  TRANSITION FROM Fill TO (Hold, Idle) := Level > 90.0;
  END_TRANSITION

  ACTION Count:
    Pump := TRUE;
  END_ACTION
END_SFC
END_PROGRAM
`

func TestDocumentSymbolsSFC(t *testing.T) {
	syms := documentSymbols("file:///filler.sfc", symSFC)
	wantOutline(t, syms, `
Filler [2] PROGRAM
  VAR_EXTERNAL [3]
    Level [13] REAL
    Pump [13] BOOL
  Idle [24] INITIAL_STEP
  Fill [24] STEP
  Hold [24] STEP
  Idle → Fill [25] t_fill: Level < 10.0
  Fill → (Hold, Idle) [25] Level > 90.0
  Count [6] ACTION
`)
	wantRange(t, syms, "Fill", "10:3-13:11", "10:8-10:12")
	wantRange(t, syms, "Idle → Fill", "18:3-19:17", "18:14-18:20")
	// An unnamed transition selects its TRANSITION keyword.
	wantRange(t, syms, "Fill → (Hold, Idle)", "20:3-21:17", "20:3-20:13")
	wantRange(t, syms, "Count", "23:3-25:13", "23:10-23:15")
}

// TestDocumentSymbolsWhileTyping: a buffer that does not parse keeps its
// outline — the declarations always, and every diagram element that still
// parses.
func TestDocumentSymbolsWhileTyping(t *testing.T) {
	t.Run("st body mid-edit", func(t *testing.T) {
		// typingSrc is recover_test's: `settle.` typed on a fresh line.
		src := typingSrc("settle.")
		if a := analyze(src, "", 0); len(a.Diags) == 0 {
			t.Fatal("the fixture parses; it should not")
		}
		syms := documentSymbols("file:///plant.st", src)
		if findSym(syms, "settle") == nil || findSym(syms, "Level") == nil {
			t.Errorf("declarations lost on a parse error:\n%s", outlineText(syms))
		}
	})
	t.Run("st VAR block mid-edit", func(t *testing.T) {
		// END_VAR not typed yet, the last declaration half written: the
		// skeleton does not parse either, the outline still lists them.
		src := "PROGRAM P\nVAR\n    a : INT;\n    b : BO\n    c\nIF a > 0 THEN b := TRUE; END_IF;\nEND_PROGRAM\n"
		syms := documentSymbols("file:///p.st", src)
		wantOutline(t, syms, `
P [2] PROGRAM
  VAR [3]
    a [13] INT
    b [13] BO
`)
	})
	t.Run("fbd statement mid-edit", func(t *testing.T) {
		src := strings.Replace(symFBD, "  Run := latch\n", "  Run := AND(latch,\n", 1)
		syms := documentSymbols("file:///main.fbd", src)
		main := findSym(syms, "Main")
		var names []string
		for _, c := range main.Children {
			names = append(names, c.Name)
		}
		// Run's statement is broken (and takes the next line with it); the
		// statements before and after it survive.
		got := strings.Join(names, " ")
		for _, want := range []string{"latch", "t1", "Delay"} {
			if !strings.Contains(" "+got+" ", " "+want+" ") {
				t.Errorf("statement %q lost: %s", want, got)
			}
		}
	})
	t.Run("sfc header and step mid-edit", func(t *testing.T) {
		// END_VAR missing (sfc.Parse fails on the header) and a step
		// without its END_STEP: the declarations, the other steps, the
		// transitions and the action all stay.
		src := strings.Replace(symSFC, "    Pump  : BOOL;\nEND_VAR\n", "    Pump  : BOOL;\n", 1)
		src = strings.Replace(src, "  STEP Hold:\n  END_STEP\n", "  STEP Hold\n", 1)
		syms := documentSymbols("file:///filler.sfc", src)
		for _, want := range []string{"Level", "Pump", "Idle", "Fill", "Idle → Fill", "Count"} {
			if findSym(syms, want) == nil {
				t.Errorf("%q lost:\n%s", want, outlineText(syms))
			}
		}
	})
	t.Run("unterminated POU", func(t *testing.T) {
		src := "PROGRAM P\nVAR\n    a : INT;\nEND_VAR\na := 1;\n"
		syms := documentSymbols("file:///p.st", src)
		wantRange(t, syms, "P", "1:1-6:1", "1:9-1:10")
	})
}

// TestDocumentSymbolsRequest drives textDocument/documentSymbol through
// the JSON-RPC session: advertised at initialize, answered for an open
// document (a parse error included), [] for a test suite, null for a
// document the server does not have.
func TestDocumentSymbolsRequest(t *testing.T) {
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	if !init.Capabilities.DocumentSymbolProvider {
		t.Fatal("documentSymbolProvider not advertised")
	}
	s.send("initialized", map[string]any{}, false)

	request := func(uri string) json.RawMessage {
		id := s.send("textDocument/documentSymbol", DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}, true)
		return s.recvResponse(id)
	}
	const uri = "file:///plant.st"
	s.open(uri, typingSrc("settle."))
	var syms []DocumentSymbol
	if err := json.Unmarshal(request(uri), &syms); err != nil {
		t.Fatal(err)
	}
	if findSym(syms, "Plant") == nil || findSym(syms, "settle") == nil || findSym(syms, "Tank_Type") == nil {
		t.Errorf("outline of a buffer mid-edit:\n%s", outlineText(syms))
	}

	const test = "file:///batch_test.yaml"
	s.open(test, "tests: []\n")
	if got := string(request(test)); got != "[]" {
		t.Errorf("test suite outline = %s, want []", got)
	}
	if got := string(request("file:///nope.st")); got != "null" {
		t.Errorf("unknown document = %s, want null", got)
	}
}

// TestDocumentSymbolsExamplesWellFormed outlines every example program and
// checks what VS Code enforces (a non-empty name, the selection inside the
// range) plus what the breadcrumbs need (each child inside its parent).
func TestDocumentSymbolsExamplesWellFormed(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".st", ".fbd", ".ld", ".sfc":
		default:
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		syms := documentSymbols("file:///"+filepath.ToSlash(path), string(src))
		if len(syms) == 0 {
			t.Errorf("%s: empty outline", path)
		}
		checkWellFormed(t, path, syms, nil)
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("only %d example files found under %s", n, root)
	}
}

func checkWellFormed(t *testing.T, path string, syms []DocumentSymbol, parent *DocumentSymbol) {
	t.Helper()
	in := func(r, outer Range) bool {
		return !before(r.Start, outer.Start) && !before(outer.End, r.End)
	}
	for i := range syms {
		s := &syms[i]
		if strings.TrimSpace(s.Name) == "" {
			t.Errorf("%s: a symbol with no name at %+v", path, s.Range)
		}
		if before(s.Range.End, s.Range.Start) || !in(s.SelectionRange, s.Range) {
			t.Errorf("%s: %s selection %+v outside range %+v", path, s.Name, s.SelectionRange, s.Range)
		}
		if parent != nil && !in(s.Range, parent.Range) {
			t.Errorf("%s: %s %+v outside its parent %s %+v", path, s.Name, s.Range, parent.Name, parent.Range)
		}
		checkWellFormed(t, path, s.Children, s)
	}
}

// #202: REGION … END_REGION shows in the outline under its POU, nested
// regions under theirs, after the VAR sections (sorted by position); a
// variable called Region is not a region.
func TestDocumentSymbolsRegions(t *testing.T) {
	src := `PROGRAM Mixer
VAR x : INT; Region : INT; END_VAR
REGION Fill the tank
    x := 1;
    REGION valves
        x := 2;
    END_REGION
END_REGION;
Region := 3;
REGION Drain
    x := 0;
END_REGION
END_PROGRAM
`
	syms := documentSymbols("file:///p.st", src)
	wantOutline(t, syms, `
Mixer [2] PROGRAM
  VAR [3]
    x [13] INT
    Region [13] INT
  Fill the tank [3] REGION
    valves [3] REGION
  Drain [3] REGION
`)
	wantRange(t, syms, "Fill the tank", "3:1-8:12", "3:1-3:21")
	wantRange(t, syms, "valves", "5:5-7:15", "5:5-5:18")
}
