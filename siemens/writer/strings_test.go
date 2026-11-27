package writer

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStringsGolden: every STRING construct the writer writes, as the
// golden that compiled 0/0 on TIA (testdata/strings/README.md).
func TestStringsGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/strings/main.st")
	if err != nil {
		t.Fatal(err)
	}
	out := goldenInput(t, "strings", stringsInput(string(src)))
	if len(out.Warnings) == 0 {
		t.Error("no warnings: the CONCATs and the STRING_TO_ conversions should have some")
	}
}

func stringsInput(src string) Input {
	return Input{
		Programs:    []Program{{Path: "main.st", Source: src}},
		Globals:     []Global{{Name: "Msg", Type: "STRING", Init: "'hi'"}, {Name: "Go", Type: "BOOL"}, {Name: "N", Type: "DINT"}},
		Tasks:       []Task{{Name: "main", Program: "main", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	}
}

// writeMain writes one program body over the golden's declarations and
// returns its FB source, the diagnostics and the warnings.
func writeMain(t *testing.T, body string) (string, []Diag, []Diag) {
	t.Helper()
	src, err := os.ReadFile("testdata/strings/main.st")
	if err != nil {
		t.Fatal(err)
	}
	decl, _, _ := strings.Cut(string(src), "(* the functions")
	out, diags, err := Write(stringsInput(decl + body + "\nEND_PROGRAM\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Files {
		if f.Path == "naut_main.scl" {
			return string(f.Data), diags, out.Warnings
		}
	}
	return "", diags, out.Warnings
}

// TestStringFunctionsAsTIATakesThem: formal parameters, and a clamp only
// where TIA gives an empty string and nautilus clamps (measured on the bench CPU,
// strings.go): P of MID, DELETE, REPLACE below 1, P of INSERT below 0, L
// of DELETE and REPLACE below 0. Constants are clamped by the writer; a
// length or position wider than INT is computed at 64 bits and clamped
// before it narrows.
func TestStringFunctionsAsTIATakesThem(t *testing.T) {
	for _, c := range []struct{ st, want string }{
		{"r := LEFT(s, l);", "#r := LEFT(IN := #s, L := #l);"},
		{"r := RIGHT(s, l);", "#r := RIGHT(IN := #s, L := #l);"},
		{"r := MID(s, l, 2);", "#r := MID(IN := #s, L := #l, P := 2);"},
		{"r := MID(s, l, p);", "#r := MID(IN := #s, L := #l, P := MAX(IN1 := #p, IN2 := 1));"},
		{"r := INSERT(s, 'XY', p);", "#r := INSERT(IN1 := #s, IN2 := 'XY', P := MAX(IN1 := #p, IN2 := 0));"},
		{"r := DELETE(s, l, p);", "#r := DELETE(IN := #s, L := MAX(IN1 := #l, IN2 := 0), P := MAX(IN1 := #p, IN2 := 1));"},
		{"r := REPLACE(s, 'ab', l, p);", "#r := REPLACE(IN1 := #s, IN2 := 'ab', L := MAX(IN1 := #l, IN2 := 0), P := MAX(IN1 := #p, IN2 := 1));"},
		{"k := FIND(s, 'LL');", "#k := FIND(IN1 := #s, IN2 := 'LL');"},
		{"ln := LEN(s);", "#ln := LEN(#s);"},
		{"r := MID(s, 2, 0);", "#r := MID(IN := #s, L := 2, P := 1);"},
		{"r := MID(s, 2, -4);", "#r := MID(IN := #s, L := 2, P := 1);"},
		{"r := INSERT(s, 'XY', -2);", "#r := INSERT(IN1 := #s, IN2 := 'XY', P := 0);"},
		{"r := DELETE(s, -1, 2);", "#r := DELETE(IN := #s, L := 0, P := 2);"},
		{"r := REPLACE(s, 'ab', 2, 0);", "#r := REPLACE(IN1 := #s, IN2 := 'ab', L := 2, P := 1);"},
		{"r := LEFT(s, 999);", "#r := LEFT(IN := #s, L := 254);"},
		{"r := MID(s, 2, 70000);", "#r := MID(IN := #s, L := 2, P := 255);"},
		{"r := LEFT(s, LEN(s) - 1);", "#r := LEFT(IN := #s, L := LINT_TO_INT(LIMIT(MN := LINT#0, IN := INT_TO_LINT(LEN(#s)) - 1, MX := LINT#254)));"},
		{"r := MID(s, d, li);", "#r := MID(IN := #s, L := LINT_TO_INT(LIMIT(MN := LINT#0, IN := DINT_TO_LINT(#d), MX := LINT#254)), P := LINT_TO_INT(LIMIT(MN := LINT#1, IN := #li, MX := LINT#255)));"},
		{"r := INSERT(s, t, ui);", "#r := INSERT(IN1 := #s, IN2 := #t, P := LINT_TO_INT(LIMIT(MN := LINT#0, IN := UINT_TO_LINT(#ui), MX := LINT#254)));"},
		{"r := CONCAT(CONCAT(s, '-'), t, CONCAT('<', CONCAT(r, '>')));", "#r := CONCAT(IN1 := #s, IN2 := '-', IN3 := #t, IN4 := '<', IN5 := #r, IN6 := '>');"},
		{"r := BOOL_TO_STRING(b);", "#r := SEL(G := #b, IN0 := 'FALSE', IN1 := 'TRUE');"},
		{"r := INT_TO_STRING(l);", `#r := "naut_STR_LINT"(A := INT_TO_LINT(#l));`},
		{"r := LINT_TO_STRING(li);", `#r := "naut_STR_LINT"(A := #li);`},
		{"r := ULINT_TO_STRING(ul);", `#r := "naut_STR_ULINT"(A := #ul);`},
		{"r := TO_STRING(d);", `#r := "naut_STR_LINT"(A := DINT_TO_LINT(#d));`},
		{"l := STRING_TO_INT(s);", "#l := STRING_TO_INT(#s);"},
		{"l := TO_INT(s);", "#l := STRING_TO_INT(#s);"},
		{"r := 'it$'s $$5$N';", "#r := 'it$'s $$5$N';"},
	} {
		got, diags, _ := writeMain(t, c.st)
		if len(diags) > 0 {
			t.Errorf("%s: %v", c.st, diags)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:\nwant %s\nin   %s", c.st, c.want, got)
		}
	}
}

// TestConcatOfMoreThan32: TIA's CONCAT takes IN1 … IN32.
func TestConcatOfMoreThan32(t *testing.T) {
	for n, want := range map[int]string{32: "IN32 := #s);", 33: "#r := CONCAT(IN1 := CONCAT(IN1 := #s,"} {
		ops := strings.TrimSuffix(strings.Repeat("s, ", n), ", ")
		got, diags, _ := writeMain(t, "r := CONCAT("+ops+");")
		if len(diags) > 0 || strings.Contains(got, "IN33") || !strings.Contains(got, want) {
			t.Errorf("%d operands: %v\n%s", n, diags, got)
		}
	}
	ops := make([]string, 40)
	for i := range ops {
		ops[i] = "s"
	}
	got, diags, _ := writeMain(t, "r := CONCAT("+strings.Join(ops, ", ")+");")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if strings.Contains(got, "IN33") || !strings.Contains(got, "#r := CONCAT(IN1 := CONCAT(IN1 := #s,") || !strings.Contains(got, "IN32 := #s), IN2 := CONCAT(IN1 := #s, IN2 := #s, IN3 := #s, IN4 := #s, IN5 := #s, IN6 := #s, IN7 := #s, IN8 := #s));") {
		t.Errorf("40 operands:\n%s", got)
	}
}

// TestStringWarnings: a result that may pass 254 bytes (TIA keeps the
// first 254) and a STRING_TO_ number of bad text (nautilus faults, the
// CPU uses 0 or a partial number) are warned at their line; literals that
// add up to 254 bytes or fewer are not.
func TestStringWarnings(t *testing.T) {
	for _, c := range []struct {
		st, rule, want string
	}{
		{"r := CONCAT(s, t);", StringLengthRule, "CONCAT here can differ if its result grows past 254 bytes: TIA keeps the first 254 bytes; nautilus keeps them all"},
		{"r := INSERT(s, 'x', 1);", StringLengthRule, "INSERT here"},
		{"r := REPLACE(s, 'x', 1, 1);", StringLengthRule, "REPLACE here"},
		{"r := CONCAT('" + strings.Repeat("a", 200) + "', '" + strings.Repeat("b", 55) + "');", StringLengthRule, "CONCAT here"},
		{"l := STRING_TO_INT(s);", StringParseRule, "STRING_TO_INT can differ if the text is not a plain decimal number in INT's range: nautilus stops the program (the scan faults); the PLC silently uses 0 or a partial number"},
		{"x := STRING_TO_REAL(s);", StringParseRule, "STRING_TO_REAL can differ"},
	} {
		_, diags, ws := writeMain(t, c.st)
		if len(diags) > 0 {
			t.Errorf("%s: %v", c.st, diags)
			continue
		}
		if len(ws) != 1 || ws[0].Rule != c.rule || !strings.Contains(ws[0].Message, c.want) || ws[0].Line == 0 {
			t.Errorf("%s: want one %s warning %q, got %v", c.st, c.rule, c.want, ws)
		}
	}
	for _, st := range []string{"r := CONCAT('a', 'b');", "r := CONCAT('" + strings.Repeat("a", 200) + "', '" + strings.Repeat("b", 54) + "');", "r := LEFT(s, l);", "r := INT_TO_STRING(l);"} {
		if _, diags, ws := writeMain(t, st); len(diags) > 0 || len(ws) > 0 {
			t.Errorf("%s: want no warning, got %v %v", st, diags, ws)
		}
	}
}

// TestStringRefusals: what TIA cannot hold is refused as TIA's limit
// (a literal over 254 bytes); what the writer cannot write yet is a gap
// naming its issue, never the author's error.
func TestStringRefusals(t *testing.T) {
	for _, c := range []struct{ st, want string }{
		{"r := '" + strings.Repeat("a", 255) + "';", "this STRING literal is 255 bytes; TIA's String holds at most 254"},
		{"r := 'café';", "the Siemens writer doesn't translate a STRING literal with characters outside ASCII yet (#1303)"},
		{"r := REAL_TO_STRING(x);", "the Siemens writer doesn't translate REAL_TO_STRING yet (#1375): TIA writes +2.150000E+1 where nautilus writes 21.5"},
		{"r := LREAL_TO_STRING(lx);", "LREAL_TO_STRING yet (#1375)"},
		{"r := TO_STRING(x);", "REAL_TO_STRING yet (#1375)"},
		{"b := STRING_TO_BOOL(s);", "STRING_TO_BOOL yet (#1375): TIA has no such conversion in SCL"},
		{"r := TIME_TO_STRING(T#1s);", "TIME_TO_STRING yet (#1375)"},
		{"r := WORD_TO_STRING(16#FF);", "WORD_TO_STRING yet (#1375)"},
	} {
		_, diags, _ := writeMain(t, c.st)
		if len(diags) == 0 || !strings.Contains(fmt.Sprint(diags), c.want) {
			t.Errorf("%s: want %q, got %v", c.st, c.want, diags)
		}
	}
}

// TestStringTagInit: a STRING tag's start value is text, even one that
// reads like a TIME (it was written as T#5s, a TIME literal).
func TestStringTagInit(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
		bad  string
	}{
		{"T#5s", "'T#5s'", ""},
		{"it's", "'it$'s'", ""},
		{strings.Repeat("a", 254), "'" + strings.Repeat("a", 254) + "'", ""},
		{strings.Repeat("a", 255), "", "255 bytes"},
		{"café", "", "#1303"},
	} {
		got, err := InitLiteral(c.v, "STRING")
		if c.bad != "" {
			if err == nil || !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%q: want error %q, got %q %v", c.v, c.bad, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: want %s, got %s %v", c.v, c.want, got, err)
		}
	}
	if got, err := InitLiteral("T#5s", "TIME"); err != nil || got != "T#5s" {
		t.Errorf("a TIME keeps its literal: %s %v", got, err)
	}
}

// #1432: an integer start value is written as the integer it is (the
// manifest loader no longer turns it into a float64, which rounded one
// above 2^53), and as milliseconds on a TIME.
func TestIntegerInitLiteralIsExact(t *testing.T) {
	for _, c := range []struct {
		v    any
		typ  string
		want string
	}{
		{9007199254740993, "LINT", "9007199254740993"},
		{int64(-9007199254740993), "LINT", "-9007199254740993"},
		{5000, "TIME", "T#5000ms"},
		{3, "REAL", "3.0"},
		{7, "DINT", "7"},
	} {
		if got, err := InitLiteral(c.v, c.typ); err != nil || got != c.want {
			t.Errorf("InitLiteral(%v, %s) = %q, %v; want %q", c.v, c.typ, got, err, c.want)
		}
	}
}
