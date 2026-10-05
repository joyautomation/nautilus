package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sigAt asks for signature help at a 0-based position; nil when the
// server answers null.
func (s *lspSession) sigAt(uri string, line, char int) *SignatureHelp {
	s.t.Helper()
	id := s.send("textDocument/signatureHelp", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: char},
	}, true)
	raw := s.recvResponse(id)
	if string(raw) == "null" {
		return nil
	}
	var h SignatureHelp
	if err := json.Unmarshal(raw, &h); err != nil {
		s.t.Fatal(err)
	}
	return &h
}

// paramLabels slices each parameter's span out of the signature label.
func paramLabels(t *testing.T, si SignatureInformation) []string {
	t.Helper()
	var out []string
	for _, p := range si.Parameters {
		if p.Label[0] < 0 || p.Label[1] > len(si.Label) || p.Label[0] > p.Label[1] {
			t.Fatalf("parameter span %v outside label %q", p.Label, si.Label)
		}
		out = append(out, si.Label[p.Label[0]:p.Label[1]])
	}
	return out
}

// sigCase is one cursor position and what the widget must show there.
type sigCase struct {
	name   string
	line   string // appended to the document; the cursor sits at its end
	label  string // "" = the server must answer null
	params []string
	active int
}

// sigSrc extends typingHead with a user FUNCTION and FUNCTION_BLOCK so
// every call kind has something to resolve.
const sigLib = `FUNCTION Scale : REAL
VAR_INPUT
  Raw : INT;
  Span : REAL;
END_VAR
Scale := INT_TO_REAL(Raw) * Span;
END_FUNCTION

FUNCTION_BLOCK Motor
VAR_INPUT
  Start : BOOL;
END_VAR
VAR_IN_OUT
  Tank : Tank_Type;
END_VAR
VAR_OUTPUT
  Run : BOOL;
END_VAR
Run := Start;
END_FUNCTION_BLOCK

`

func sigSrc(line string) (string, int, int) {
	head := strings.Replace(typingHead, "PROGRAM Plant\nVAR\n", sigLib+"PROGRAM Plant\nVAR\n  m1 : Motor;\n", 1)
	src := head + line + "\nEND_PROGRAM\n"
	return src, strings.Count(head, "\n"), len(line)
}

func runSigCases(t *testing.T, s *lspSession, uri string, build func(string) (string, int, int), cases []sigCase) {
	t.Helper()
	for _, c := range cases {
		src, line, col := build(c.line)
		s.change(uri, src)
		h := s.sigAt(uri, line, col)
		if c.label == "" {
			if h != nil {
				t.Errorf("%s: %q → %+v, want null", c.name, c.line, h)
			}
			continue
		}
		if h == nil || len(h.Signatures) != 1 {
			t.Errorf("%s: %q → %+v, want one signature", c.name, c.line, h)
			continue
		}
		si := h.Signatures[0]
		if si.Label != c.label {
			t.Errorf("%s: label = %q, want %q", c.name, si.Label, c.label)
		}
		if got := paramLabels(t, si); strings.Join(got, "|") != strings.Join(c.params, "|") {
			t.Errorf("%s: params = %q, want %q", c.name, got, c.params)
		}
		if h.ActiveParameter != c.active {
			t.Errorf("%s: activeParameter = %d, want %d", c.name, h.ActiveParameter, c.active)
		}
	}
}

func TestSignatureHelpCallKinds(t *testing.T) {
	const uri = "file:///plant.st"
	s := startSession(t)
	src, _, _ := sigSrc("")
	s.open(uri, src)

	limit := "LIMIT(MN : ANY_NUM, IN : ANY_NUM, MX : ANY_NUM) : ANY_NUM"
	limitParams := []string{"MN : ANY_NUM", "IN : ANY_NUM", "MX : ANY_NUM"}
	ton := "TON(IN : BOOL, PT : TIME) => Q : BOOL, ET : TIME"
	tonParams := []string{"IN : BOOL", "PT : TIME", "Q : BOOL", "ET : TIME"}
	motor := "Motor(Start : BOOL, Tank : Tank_Type) => Run : BOOL"
	motorParams := []string{"Start : BOOL", "Tank : Tank_Type", "Run : BOOL"}

	runSigCases(t, s, uri, sigSrc, []sigCase{
		// Every line below is mid-edit: the document does not parse.
		{"builtin", "Level := LIMIT(", limit, limitParams, 0},
		{"after comma", "Level := LIMIT(0.0, ", limit, limitParams, 1},
		{"third", "Level := LIMIT(0.0, Level, ", limit, limitParams, 2},
		{"lowercase", "Level := limit(0.0, ", limit, limitParams, 1},
		{"nested inner", "Level := LIMIT(0.0, SQRT(", "SQRT(IN : REAL) : REAL", []string{"IN : REAL"}, 0},
		{"nested closed", "Level := LIMIT(0.0, SQRT(MAX(Level, 1.0)), ", limit, limitParams, 2},
		{"grouping paren", "Level := LIMIT(0.0, (Level + ", limit, limitParams, 1},
		{"variadic grows", "Level := MAX(1.0, 2.0, 3.0, ",
			"MAX(IN1 : ANY_NUM, IN2 : ANY_NUM, IN3 : ANY_NUM, IN4 : ANY_NUM, …) : ANY_NUM",
			[]string{"IN1 : ANY_NUM", "IN2 : ANY_NUM", "IN3 : ANY_NUM", "IN4 : ANY_NUM"}, 3},
		{"selector", "Level := SEL(Full, ", "SEL(G : BOOL, IN0 : ANY, IN1 : ANY) : ANY",
			[]string{"G : BOOL", "IN0 : ANY", "IN1 : ANY"}, 1},
		{"conversion", "Level := INT_TO_REAL(", "INT_TO_REAL(IN : INT) : REAL", []string{"IN : INT"}, 0},
		{"user function", "Level := Scale(3, ", "Scale(Raw : INT, Span : REAL) : REAL",
			[]string{"Raw : INT", "Span : REAL"}, 1},
		{"fb open", "settle(", ton, tonParams, 0},
		{"fb named pin", "settle(IN := Full, PT := ", ton, tonParams, 1},
		{"fb named first", "settle(PT := T#1s, IN := ", ton, tonParams, 0},
		{"fb next unbound", "settle(IN := Full, ", ton, tonParams, 1},
		{"fb next unbound skips bound", "settle(PT := T#1s, ", ton, tonParams, 0},
		{"fb output pin", "settle(IN := Full, ET => ", ton, tonParams, 3},
		{"fb pin value is a call", "settle(IN := Full, PT := INT_TO_TIME(", "INT_TO_TIME(IN : INT) : TIME", []string{"IN : INT"}, 0},
		{"user fb", "m1(Start := Full, ", motor, motorParams, 1},
		{"user fb named", "m1(Tank := Tank, Start := ", motor, motorParams, 0},
		{"user fb output", "m1(Run => ", motor, motorParams, 2},
		{"string and comment commas", "Level := LIMIT('a, b', (* c, d *) ", limit, limitParams, 1},
		{"unknown function", "Level := Bogus(", "", nil, 0},
		{"unknown inside known", "Level := LIMIT(0.0, Bogus(", "", nil, 0},
		{"not a callable", "Level := Level(", "", nil, 0},
		{"bare grouping", "Level := (Level + ", "", nil, 0},
		{"closed call", "Level := LIMIT(0.0, Level, 1.0) + ", "", nil, 0},
		{"previous statement", "Level := LIMIT(0.0, Level, 1.0; Full := ", "", nil, 0},
		{"operators are not ST calls", "Full := GT(", "", nil, 0},
	})
}

// TestSignatureHelpUserFBFromLibrary resolves a block declared in a sibling
// library file — the same prelude the compile uses.
func TestSignatureHelpUserFBFromLibrary(t *testing.T) {
	dir := t.TempDir()
	lib := "FUNCTION_BLOCK Valve\nVAR_INPUT\n  Open : BOOL;\nEND_VAR\nVAR_OUTPUT\n  Pos : REAL;\nEND_VAR\nPos := 0.0;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION Clamp01 : REAL\nVAR_INPUT\n  X : REAL;\nEND_VAR\nClamp01 := LIMIT(0.0, X, 1.0);\nEND_FUNCTION\n"
	if err := os.WriteFile(filepath.Join(dir, "valves.st"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + dir + "/main.st"
	s := startSession(t)
	build := func(line string) (string, int, int) {
		head := "PROGRAM Main\nVAR\n  v1 : Valve;\n  r : REAL;\nEND_VAR\n"
		return head + line + "\nEND_PROGRAM\n", strings.Count(head, "\n"), len(line)
	}
	src, _, _ := build("")
	s.open(uri, src)
	runSigCases(t, s, uri, build, []sigCase{
		{"library fb", "v1(", "Valve(Open : BOOL) => Pos : REAL", []string{"Open : BOOL", "Pos : REAL"}, 0},
		{"library function", "r := Clamp01(", "Clamp01(X : REAL) : REAL", []string{"X : REAL"}, 0},
	})
}

// TestSignatureHelpFBD answers inside a netlist line of a .fbd file: the
// inline `inst : TYPE(` form, a call continued over lines, the operator
// blocks, and a function — with the line mid-edit (the netlist does not
// transpile, so the declarations come from the last version that did).
func TestSignatureHelpFBD(t *testing.T) {
	const uri = "file:///level.fbd"
	head := `PROGRAM Level
VAR_EXTERNAL
  Run : BOOL; Level : REAL; Out : REAL;
END_VAR
FBD
  t1 : TON(IN := Run, PT := T#5S)
`
	tail := "\nEND_FBD\nEND_PROGRAM\n"
	build := func(line string) (string, int, int) {
		lines := strings.Split(line, "\n")
		return head + line + tail, strings.Count(head, "\n") + len(lines) - 1, len(lines[len(lines)-1])
	}
	s := startSession(t)
	s.open(uri, head+"  Out := Level"+tail)
	h := s.sigAt(uri, 5, len("  t1 : TON(IN := Run, PT := "))
	if h == nil || h.Signatures[0].Label != "TON(IN : BOOL, PT : TIME) => Q : BOOL, ET : TIME" || h.ActiveParameter != 1 {
		t.Fatalf("inline TON in a parsing netlist → %+v", h)
	}
	if d := h.Signatures[0].Documentation; d == nil || d.Value != "on-delay timer" {
		t.Errorf("TON documentation = %+v, want the catalog's behaviour text", d)
	}
	if d := h.Signatures[0].Parameters[2].Documentation; d == nil || d.Value != "output BOOL" {
		t.Errorf("Q documentation = %+v, want its direction and type", d)
	}

	runSigCases(t, s, uri, build, []sigCase{
		{"operator", "  Run := GT(Level, ", "GT(IN1 : ANY, IN2 : ANY) : BOOL", []string{"IN1 : ANY", "IN2 : ANY"}, 1},
		{"variadic operator", "  Run := AND(Run, Run, ", "AND(IN1 : ANY_BIT, IN2 : ANY_BIT, IN3 : ANY_BIT, …) : ANY_BIT",
			[]string{"IN1 : ANY_BIT", "IN2 : ANY_BIT", "IN3 : ANY_BIT"}, 2},
		{"function in netlist", "  Out := LIMIT(0.0, ", "LIMIT(MN : ANY_NUM, IN : ANY_NUM, MX : ANY_NUM) : ANY_NUM",
			[]string{"MN : ANY_NUM", "IN : ANY_NUM", "MX : ANY_NUM"}, 1},
	})

	src, line, col := build("  tic : PID(AUTO := Run,\n            PV := ")
	s.change(uri, src)
	h = s.sigAt(uri, line, col)
	if h == nil {
		t.Fatal("PID continued over lines → null")
	}
	si := h.Signatures[0]
	if !strings.HasPrefix(si.Label, "PID(AUTO : BOOL, PV : REAL, SP : REAL,") || !strings.Contains(si.Label, " => CV : REAL,") {
		t.Errorf("PID label = %q", si.Label)
	}
	if got := paramLabels(t, si); h.ActiveParameter != 1 || got[1] != "PV : REAL" {
		t.Errorf("PID active = %d (%q), want 1 (PV)", h.ActiveParameter, got)
	}
}

func TestSignatureHelpCapability(t *testing.T) {
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	p := init.Capabilities.SignatureHelpProvider
	if p == nil || strings.Join(p.TriggerCharacters, "") != "(," || strings.Join(p.RetriggerCharacters, "") != "," {
		t.Fatalf("signatureHelpProvider = %+v", p)
	}
}

func TestCallAt(t *testing.T) {
	for _, c := range []struct {
		text   string
		name   string
		commas int
		named  string
	}{
		{"x := LIMIT(", "LIMIT", 0, ""},
		{"x := LIMIT(a, ", "LIMIT", 1, ""},
		{"x := LIMIT(a, F(b, c), ", "LIMIT", 2, ""},
		{"x := LIMIT(a, F(b, c", "F", 1, ""},
		{"x := CONCAT('(,', ", "CONCAT", 1, ""},
		{"x := CONCAT(// a ( b,\n 'x', ", "CONCAT", 1, ""},
		{"t1(IN := a, PT := ", "t1", 1, "PT"},
		{"t1(IN := a = b, ", "t1", 1, ""},
		{"t1(IN := F(x) , Q => ", "t1", 1, "Q"},
		{"x := (a + ", "", 0, ""},
		{"x := LIMIT(a, b, c)", "", 0, ""},
		{"x := LIMIT(a; y := ", "", 0, ""},
		{"a.b(", "", 0, ""},
	} {
		lines := strings.Split(c.text, "\n")
		f := callAt(c.text, Position{Line: len(lines) - 1, Character: len(lines[len(lines)-1])})
		if c.name == "" {
			if f != nil {
				t.Errorf("%q → %+v, want none", c.text, f)
			}
			continue
		}
		if f == nil || f.name != c.name || f.commas != c.commas || f.named != c.named {
			t.Errorf("%q → %+v, want %s commas=%d named=%q", c.text, f, c.name, c.commas, c.named)
		}
	}
}
