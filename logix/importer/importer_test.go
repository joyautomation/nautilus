package importer

import (
	"fmt"
	"github.com/joyautomation/nautilus/internal/dialect"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/writer"
)

var genName = regexp.MustCompile(`\b(rt|ft|en)_[A-Za-z0-9_]+`)

func norm(s string) string { return genName.ReplaceAllString(s, "<gen>") }

// libsOf returns the imported project's library sources.
func libsOf(p *Project) []string {
	var paths []string
	for path := range p.Files {
		if strings.HasPrefix(path, "lib/") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var libs []string
	for _, path := range paths {
		libs = append(libs, string(p.Files[path]))
	}
	return libs
}

func mustImport(t *testing.T, doc []byte) *Project {
	t.Helper()
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Import(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// rungTexts lists a routine's rung texts, NOP-only rungs left out (the
// import drops them: nothing to carry).
func rungTexts(f *l5x.File, program, routine string) []string {
	var out []string
	for _, p := range f.Controller.Programs {
		if p.Name != program {
			continue
		}
		for _, r := range p.Routines {
			if r.Name != routine {
				continue
			}
			for _, rg := range r.Rungs {
				if t := strings.TrimSpace(rg.Text); t == "NOP();" || t == ";" || t == "" {
					continue
				}
				out = append(out, norm(rg.Text))
			}
		}
	}
	return out
}

// identity imports doc, writes the imported program back and compares
// the rung text with doc's, modulo generated names.
func identity(t *testing.T, doc []byte, program, file string) *Project {
	t.Helper()
	orig, err := l5x.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := mustImport(t, doc)
	src, ok := p.Files[file]
	if !ok {
		var names []string
		for n := range p.Files {
			names = append(names, n)
		}
		t.Fatalf("no %s in the import; files: %v", file, names)
	}
	for _, n := range p.Notes {
		if n.Rung >= 0 {
			t.Errorf("note: %s", n)
		}
	}
	out, diags, err := writer.Write(string(src), writer.Options{Controller: orig.Controller.Name, Libs: libsOf(p)})
	if err != nil {
		t.Fatalf("writing the import back: %v\n%s", err, src)
	}
	for _, d := range diags {
		t.Errorf("writer: %s", d)
	}
	if t.Failed() {
		t.Fatalf("source:\n%s", src)
	}
	back, err := l5x.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	want := rungTexts(orig, program, "MainRoutine")
	got := rungTexts(back, program, "MainRoutine")
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Errorf("rung text after import+write differs\n got:\n  %s\nwant:\n  %s\nsource:\n%s", strings.Join(got, "\n  "), strings.Join(want, "\n  "), src)
	}
	return p
}

func TestDemoLineImportsAndWritesBack(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "lang", "l5x", "testdata", "demoline.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	p := identity(t, doc, "MainProgram", "MainProgram.ld")
	src := string(p.Files["MainProgram.ld"])
	// The fixture's tags are program-scoped, so they come back as VAR,
	// seeds and descriptions included.
	for _, want := range []string{
		"PROGRAM MainProgram",
		"VAR\n",
		"HiLevelAlm : BOOL; (* LAH-101 high-level alarm active *)",
		"HiLevelSP  : REAL := 85.0;",
		"RUNG r0 (* P-101 pump seal-in",
		"[ StartPB | RunCmd ] /StopPB ( RunCmd )",
		"GE(LevelPct, HiLevelSP) ( HiLevelAlm )",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	man := string(p.Files["nautilus.yaml"])
	for _, want := range []string{"program: MainProgram.ld", "tag-files: [tags/logix.yaml]", "controller: DemoLine", "task: MainTask"} {
		if !strings.Contains(man, want) {
			t.Errorf("manifest lacks %q:\n%s", want, man)
		}
	}
	if p.Routines != 1 || p.Complete != 1 || p.Rungs != 2 || p.Imported != 2 {
		t.Errorf("stats %+v", *p)
	}
}

// The writer's whole v1 subset, written and imported back: edges at and
// away from the head, a TOF's continuation, a counter with its reset and
// a variable preset, rewritten members, a timer as the last element.
func TestSubsetGoldenImportsAndWritesBack(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "subset.golden.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	p := identity(t, doc, "MainProgram", "MainProgram.ld")
	src := string(p.Files["MainProgram.ld"])
	for _, want := range []string{
		"+Start [ Remote [ /Auto | Bits[1] ] | Local ] ( Ready )",
		"Auto +Local -Remote ( Pulse )",
		"Run t1:TON(PT := T#10S) ( Dwelling )",
		"Run t2:TOF(PT := Dwell) /Fault ( Dropped )",
		"+Done c1:CTU(PV := Target, R := Reset) ( S Over ) ( R Maint )",
		"[ t1.Q | /c1.Q ] GT(t1.ET, 500) LT(c1.CV, Target) Bits[3] ( Maint )",
		"Fault t3:TON(PT := T#500MS)",
		"Dwell",
		": TIME;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	if strings.Contains(src, "rt_") || strings.Contains(src, "ft_") {
		t.Errorf("generated edge tags leaked into the source:\n%s", src)
	}
	tags := string(p.Files["tags/logix.yaml"])
	if !strings.Contains(tags, "name: Run, role: output") {
		t.Errorf("Run is written by a coil; should be an output:\n%s", tags)
	}
	if !strings.Contains(tags, "name: Start, role: input") {
		t.Errorf("Start should be an input:\n%s", tags)
	}
}

func conformance(t *testing.T, name string) (src string, libs []string, program string) {
	t.Helper()
	dir := filepath.Join("..", "writer", "testdata", "conformance", name)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if ext := filepath.Ext(e.Name()); ext == ".ld" || ext == ".st" {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			if strings.Contains(string(b), "PROGRAM ") {
				src = string(b)
				program = programName(src)
			} else {
				libs = append(libs, string(b))
			}
		}
	}
	libDir := filepath.Join(dir, "lib")
	if ents, err := os.ReadDir(libDir); err == nil {
		for _, e := range ents {
			b, _ := os.ReadFile(filepath.Join(libDir, e.Name()))
			libs = append(libs, string(b))
		}
	}
	return
}

func programName(src string) string {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PROGRAM ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "PROGRAM "))
		}
	}
	return ""
}

func TestUDTConformanceImportsAndWritesBack(t *testing.T) {
	src, libs, program := conformance(t, "udt")
	doc, diags, err := writer.Write(src, writer.Options{Controller: "C", Libs: libs})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v", err, diags)
	}
	p := identity(t, doc, program, program+".ld")
	if _, ok := p.Files["lib/logix_types.st"]; !ok {
		t.Errorf("no types file; files: %v", keys(p.Files))
	}
	if !strings.Contains(string(p.Files["tags/logix.yaml"]), "type:") {
		t.Errorf("typed tags expected:\n%s", p.Files["tags/logix.yaml"])
	}
}

func TestAOIConformanceImportsAndWritesBack(t *testing.T) {
	src, libs, program := conformance(t, "aoi")
	doc, diags, err := writer.Write(src, writer.Options{Controller: "C", Libs: libs})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v", err, diags)
	}
	orig, _ := l5x.Parse(doc)
	p := identity(t, doc, program, program+".ld")
	fb, ok := p.Files["lib/MotorStarter.ld"]
	if !ok {
		t.Fatalf("no FUNCTION_BLOCK; files: %v", keys(p.Files))
	}
	for _, want := range []string{
		"FUNCTION_BLOCK MotorStarter",
		"VAR_INPUT",
		"Mode", ": INT;",
		"VAR_OUTPUT",
		"LockedOut", ": BOOL;",
		"t1", ": TON;",
		"c1", ": CTU;",
		"[ EQ(Mode, 2) AutoReq | EQ(Mode, 1) ] Permissive /FailToRun /LockedOut ( Run )",
		"Run /RunFb t1:TON(PT := T#5S) ( S FailToRun )",
		"+FailToRun c1:CTU(PV := 3, R := AND(Reset, LockedOut), Q => LockedOut)",
	} {
		if !strings.Contains(string(fb), want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
	main := string(p.Files[program+".ld"])
	if !strings.Contains(main, "m101:MotorStarter(Mode := P101_Mode, AutoReq := P101_Req, Permissive := P101_Permissive, RunFb := P101_Running, Reset := ResetFaults)") {
		t.Errorf("call:\n%s", main)
	}
	// The block's own logic survives the trip too.
	out, _, _ := writer.Write(main, writer.Options{Controller: "C", Libs: libsOf(p)})
	back, _ := l5x.Parse(out)
	if d := l5x.LogicDiff(l5x.LogicOf(orig), l5x.LogicOf(back)); len(d) > 0 {
		for _, line := range d {
			if !strings.Contains(norm(line), "<gen>") {
				t.Errorf("logic diff: %s", line)
			}
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// variety.L5X: what has no nautilus form is kept as a comment with its
// reason, the file still parses as ladder, and the ST routine is carried
// verbatim.
func TestVarietyReportsWhatItCannotCarry(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "lang", "l5x", "testdata", "variety.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	p := mustImport(t, doc)
	// The fixture is hand-built coverage, not a consistent project: it
	// names tags it never declares, and the import says so rather than
	// inventing them.
	reasons := strings.Join(p.Reasons(), " ")
	if !strings.Contains(reasons, "undefined") {
		t.Errorf("reasons %v lack undefined", p.Reasons())
	}
	var ld, st string
	for name, b := range p.Files {
		switch filepath.Ext(name) {
		case ".ld":
			if !strings.HasPrefix(name, "lib/") {
				ld = string(b)
			}
		case ".st":
			if !strings.HasPrefix(name, "lib/") {
				st = string(b)
			}
		}
	}
	if !strings.Contains(ld, "// not imported (undefined") || !strings.Contains(ld, "NOT deployable") {
		t.Errorf("the skipped rung and the warning should be in the source:\n%s", ld)
	}
	if !strings.Contains(st, "MainProgram_Scratch := MainProgram_Scratch + 1;") {
		t.Errorf("ST routine not carried:\n%s", st)
	}
	fmt.Println("variety reasons:", p.Reasons())
}

// The corpus measurement (docs/design/logix-authoring.md, brownfield):
// how much of real Logix ladder imports, what keeps the rest out, and
// whether what imports writes back identical.
//
//	NAUTILUS_L5X_CORPUS=~/some/dir go test ./logix/importer/ -run Corpus -v
func TestCorpus(t *testing.T) {
	dir := os.Getenv("NAUTILUS_L5X_CORPUS")
	if dir == "" {
		t.Skip("set NAUTILUS_L5X_CORPUS to a directory of .L5X exports")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files int
	total := &Project{}
	reasons := map[string]int{}
	detail := map[string]int{}
	samples := map[string]int{}
	var routinesComplete, identical, writerDiag, differ int
	var aoiComplete, aoiTotal int
	for _, e := range ents {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".L5X") {
			continue
		}
		f, err := l5x.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		p, err := Import(f, Options{})
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		files++
		total.Routines += p.Routines
		total.Complete += p.Complete
		total.Rungs += p.Rungs
		total.Imported += p.Imported
		for _, n := range p.Notes {
			if n.Rung >= 0 {
				reasons[n.Reason]++
				if (n.Reason == "no-output" || n.Reason == "RES") && samples[n.Reason] < 4 {
					samples[n.Reason]++
					fmt.Printf("  sample %s: %s\n", n.Reason, n.Text)
				}
				switch n.Reason {
				case "branch", "ONS", "no-output", "TON", "CTU", "type", "array", "RES", "undefined":
					why := n.Why
					if len(why) > 60 {
						why = why[:60]
					}
					detail[n.Reason+": "+why]++
				}
			}
		}
		libs := libsOf(p)
		// Every complete program routine: write it back, compare.
		for _, prog := range f.Controller.Programs {
			for _, r := range prog.Routines {
				if r.Type != "RLL" {
					continue
				}
				file := l5x.Ident(prog.Name) + ".ld"
				src, ok := p.Files[file]
				if !ok {
					file = l5x.Ident(prog.Name) + "_" + l5x.Ident(r.Name) + ".ld"
					src, ok = p.Files[file]
				}
				if !ok {
					continue
				}
				if strings.Contains(string(src), "NOT deployable") {
					continue
				}
				routinesComplete++
				out, diags, err := writer.Write(string(src), writer.Options{Controller: f.Controller.Name, Libs: libs})
				if err != nil || len(diags) > 0 {
					writerDiag++
					if writerDiag <= 5 {
						msg := fmt.Sprint(err)
						if len(diags) > 0 {
							msg = diags[0].String()
						}
						fmt.Printf("  writer refuses an import (%s/%s): %s\n", prog.Name, r.Name, msg)
					}
					continue
				}
				back, _ := l5x.Parse(out)
				backProg := strings.TrimSuffix(file, ".ld")
				if strings.Join(rungTexts(f, prog.Name, r.Name), "\n") == strings.Join(rungTexts(back, backProg, "MainRoutine"), "\n") {
					identical++
				} else {
					differ++
					if differ <= 3 {
						a, b := rungTexts(f, prog.Name, r.Name), rungTexts(back, backProg, "MainRoutine")
						for i := range a {
							if i >= len(b) || a[i] != b[i] {
								fmt.Printf("  differs (%s/%s) rung %d:\n    orig %s\n    back %s\n", prog.Name, r.Name, i, a[i], at(b, i))
								break
							}
						}
					}
				}
			}
		}
		for _, a := range f.Controller.AOIs {
			for _, r := range a.Routines {
				if r.Type == "RLL" && r.Name == "Logic" {
					aoiTotal++
					if fb, ok := p.Files["lib/"+l5x.Ident(a.Name)+".ld"]; ok && !strings.Contains(string(fb), "NOT deployable") {
						aoiComplete++
					}
				}
			}
		}
	}
	fmt.Printf("corpus: %d files\n", files)
	fmt.Printf("ladder routines %d, complete %d (%.1f%%); rungs %d, imported %d (%.1f%%)\n",
		total.Routines, total.Complete, pct(total.Complete, total.Routines), total.Rungs, total.Imported, pct(total.Imported, total.Rungs))
	fmt.Printf("AOI Logic routines %d, complete %d (%.1f%%)\n", aoiTotal, aoiComplete, pct(aoiComplete, aoiTotal))
	fmt.Printf("complete program routines written back: %d identical, %d differ, %d refused by the writer (of %d)\n", identical, differ, writerDiag, routinesComplete)
	type kv struct {
		k string
		v int
	}
	var ks []kv
	for k, v := range reasons {
		ks = append(ks, kv{k, v})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].v > ks[j].v })
	fmt.Println("rungs not carried, by reason:")
	for i, x := range ks {
		if i >= 20 {
			break
		}
		fmt.Printf("  %-14s %6d\n", x.k, x.v)
	}
	ks = nil
	for k, v := range detail {
		ks = append(ks, kv{k, v})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].v > ks[j].v })
	fmt.Println("detail:")
	for i, x := range ks {
		if i >= 24 {
			break
		}
		fmt.Printf("  %-80s %6d\n", x.k, x.v)
	}
}

// detailOf reduces a note to the shape that caused it: a timer member
// name, a bit index, the first instruction after a one-shot.
func detailOf(n Note) string {
	t := n.Text
	switch n.Reason {
	case "member":
		for _, m := range []string{".TT", ".EN", ".CU", ".OV", ".UN", ".CD", ".DN", ".ACC", ".PRE"} {
			if strings.Contains(t, m) {
				return m
			}
		}
	case "bit":
		return "bit"
	case "ONS":
		if i := strings.Index(t, "ONS("); i >= 0 {
			rest := t[i:]
			if j := strings.Index(rest, ")"); j >= 0 && j+1 < len(rest) {
				after := rest[j+1:]
				if k := strings.Index(after, "("); k > 0 {
					return "then " + after[:k]
				}
				return "then end"
			}
		}
	}
	if len(t) > 30 {
		t = t[:30]
	}
	return t
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// miniL5X wraps rung texts in a one-program export with the given
// controller tags ("Name:TYPE" or "Name:TIMER=5000").
func miniL5X(tags []string, rungs ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
`)
	for _, t := range tags {
		name, typ, _ := strings.Cut(t, ":")
		typ, pre, hasPre := strings.Cut(typ, "=")
		fmt.Fprintf(&b, `<Tag Name="%s" TagType="Base" DataType="%s" Constant="false" ExternalAccess="Read/Write">`+"\n", name, typ)
		if hasPre {
			fmt.Fprintf(&b, `<Data Format="Decorated"><Structure DataType="%s"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="%s"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>`+"\n", typ, pre)
		}
		b.WriteString("</Tag>\n")
	}
	b.WriteString("</Tags>\n<Programs>\n<Program Name=\"Main\" TestEdits=\"false\" MainRoutineName=\"MainRoutine\" Disabled=\"false\" UseAsFolder=\"false\">\n<Tags/>\n<Routines>\n<Routine Name=\"MainRoutine\" Type=\"RLL\">\n<RLLContent>\n")
	for i, r := range rungs {
		fmt.Fprintf(&b, "<Rung Number=\"%d\" Type=\"N\">\n<Text>\n<![CDATA[%s]]>\n</Text>\n</Rung>\n", i, r)
	}
	b.WriteString("</RLLContent>\n</Routine>\n</Routines>\n</Program>\n</Programs>\n<Tasks>\n<Task Name=\"MainTask\" Type=\"PERIODIC\" Rate=\"10\" Priority=\"10\" Watchdog=\"500\">\n<ScheduledPrograms>\n<ScheduledProgram Name=\"Main\"/>\n</ScheduledPrograms>\n</Task>\n</Tasks>\n</Controller>\n</RSLogix5000Content>\n")
	return []byte(b.String())
}

// Idioms a person writes that the writer never does, and how they come
// across: a one-shot at a branch leg's head, a one-shot of the whole
// condition driving one coil (the P coil), CMP with a plain comparison,
// and the timer's TT bit.
func TestHumanIdiomsImport(t *testing.T) {
	tags := []string{"a:BOOL", "b:BOOL", "c:BOOL", "x:BOOL", "y:BOOL", "z:BOOL", "n:DINT", "t:TIMER=1500"}
	doc := miniL5X(tags,
		"[XIC(a)ONS(s1) ,XIC(b) ]OTE(x);",
		"XIC(a)XIC(b)ONS(s2)OTE(y);",
		"CMP(n >= 10)OTE(z);",
		"XIC(c)TON(t,?,?);",
		"XIC(t.TT)OTE(x);",
		"XIO(t.TT)OTE(y);",
	)
	p := mustImport(t, doc)
	for _, n := range p.Notes {
		if n.Rung >= 0 {
			t.Errorf("note: %s", n)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"[ +a | b ] ( x )",
		"a b ( P y )",
		"GE(n, 10) ( z )",
		"c t:TON(PT := T#1500MS)",
		"t.IN /t.Q ( x )",
		"[ /t.IN | t.Q ] ( y )",
		"scan: 10ms",
	} {
		if !strings.Contains(src+string(p.Files["nautilus.yaml"]), want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	// Written back, the P coil is the ONS idiom again and the CMP is the
	// compare it meant; TT becomes the two bits it is made of.
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, _ := l5x.Parse(out)
	got := rungTexts(back, "Main", "MainRoutine")
	want := []string{
		"[XIC(a)ONS(<gen>) ,XIC(b) ]OTE(x);",
		"XIC(a)XIC(b)ONS(<gen>)OTE(y);",
		"GE(n,10)OTE(z);",
		"XIC(c)TON(t,?,?);",
		"XIC(t.EN)XIO(t.DN)OTE(x);",
		"[XIO(t.EN) ,XIC(t.DN) ]OTE(y);",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("written back:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// What is refused is refused with its reason, and the program still parses.
func TestRefusalsKeepTheProgramValid(t *testing.T) {
	tags := []string{"a:BOOL", "b:BOOL", "n:DINT", "m:DINT", "t:TIMER=1000", "c:COUNTER=3", "arr:DINT"}
	doc := miniL5X(tags,
		"XIC(a)COP(n,m,1);",
		"XIC(a)XIC(b)ONS(s)OTL(b);",
		"XIC(n.3)OTE(m.12);",
		"XIC(a)OTE(b)XIC(b);",
		"XIC(a)CTU(c,?,?);",
		"XIC(b)OTE(a);",
		"XIC(a)RES(c);",
		"XIC(a)JSR(Sub,0);",
	)
	p := mustImport(t, doc)
	reasons := strings.Join(p.Reasons(), " ")
	for _, want := range []string{"COP 1", "RES 1", "JSR 1"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("reasons %v lack %q", p.Reasons(), want)
		}
	}
	src := string(p.Files["Main.ld"])
	if !strings.Contains(src, "NOT deployable") || !strings.Contains(src, "// not imported (COP: a data operation with no assignment form): XIC(a)COP(n,m,1);") {
		t.Errorf("source should carry the refusals:\n%s", src)
	}
	// The compound one-shot driving a latch: a pulse tag of its own.
	if !strings.Contains(src, "a b ( P os_r1_1 )") || !strings.Contains(src, "os_r1_1 ( S b )") {
		t.Errorf("compound one-shot:\n%s", src)
	}
	// Bits of integers keep their spelling.
	if !strings.Contains(src, "n.3 ( m.12 )") {
		t.Errorf("bit access:\n%s", src)
	}
	if _, _, err := writer.Write(src, writer.Options{Controller: "Mini"}); err != nil {
		t.Errorf("the partial program must still write: %v\n%s", err, src)
	}
	// XIC(a)OTE(b)XIC(b): the trailing contact drives nothing in Logix
	// either, so the rung imports as a ( b ).
	if p.Complete != 0 || p.Imported != 5 {
		t.Errorf("complete %d imported %d", p.Complete, p.Imported)
	}
}

// Logix's 2-D habits — an output leg inside a branch, a timer inside a
// leg, a one-shot inside a leg, parallel timers — come across as the
// rungs they are, one per output leg, sharing the condition.
func TestBranchOutputsAndHoistedBlocks(t *testing.T) {
	tags := []string{"a:BOOL", "b:BOOL", "c:BOOL", "x:BOOL", "y:BOOL", "z:BOOL", "n:DINT", "m:DINT", "t1:TIMER=1000", "t2:TIMER=2000"}
	doc := miniL5X(tags,
		"XIC(c)[XIC(a)OTE(x) ,XIC(b) ]OTE(y);",
		"XIC(c)[XIC(a)TON(t1,?,?) ,XIC(b) ]OTE(y);",
		"XIC(c)[TON(t1,?,?) ,TON(t2,?,?) ];",
		"XIC(c)[XIC(a)ONS(s) ,XIC(b) ]OTE(z);",
		"XIC(c)OSR(s2,x)OTE(y);",
		"XIC(c)OTE(x)XIC(a);",
		"XIC(c)[MOVE(1,n) ,MOVE(2,m) ];",
		"[GT(n,5) ,LT(n,1) ][MOVE(3,n) ,MOVE(4,m) ];",
	)
	p := mustImport(t, doc)
	for _, nt := range p.Notes {
		if nt.Rung >= 0 {
			t.Errorf("note: %s", nt)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"c a ( x )",
		"c [ a | b ] ( y )",
		"c a t1:TON(PT := T#1S)",
		"c t1:TON(PT := T#1S)",
		"c t2:TON(PT := T#2S)",
		"c a ( P os_r3_1 )",
		"c [ os_r3_1 | b ] ( z )",
		"c ( P x )",
		"c ( y )",
		"c ( x )",
		"c [ { n := 1 } | { m := 2 } ]",
		"[ GT(n, 5) | LT(n, 1) ] [ { n := 3 } | { m := 4 } ]",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	if strings.Count(src, "RUNG ") != 13 {
		t.Errorf("want 13 rungs:\n%s", src)
	}
}

// Data instructions come across as assignments and write back as the
// same instructions; CPT and CMP expressions survive with their function
// names translated both ways.
func TestDataInstructionsImportAndWriteBack(t *testing.T) {
	tags := []string{"a:BOOL", "b:BOOL", "n:DINT", "m:DINT", "r:REAL", "q:REAL"}
	doc := miniL5X(tags,
		"XIC(a)MOVE(m,n)MOVE(1.5,r);",
		"XIC(a)ADD(n,1,n)SUB(n,m,m)MUL(r,2.0,r)DIV(r,q,q)ABS(q,r);",
		"XIC(a)CPT(n,(n + 3) * m - 7)CPT(r,SQR(q) + ABS(r) ** 2.0)CPT(n,n MOD 4)CPT(r,TRN(q));",
		"CMP(n + 1 > m)CMP(r * 2.0 <= q)CMP(n MOD 2 = 0)OTE(b);",
		"XIC(a)[MOVE(1,n) ,XIC(b) MOVE(2,n) ]OTE(b);",
		"XIC(a)CLR(n)CLR(r)NEG(m,n)XPY(r,2.0,q);",
		"LIMIT(0,n,100)OTE(b);",
	)
	p := mustImport(t, doc)
	for _, nt := range p.Notes {
		if nt.Rung >= 0 {
			t.Errorf("note: %s", nt)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"a { n := m } { r := 1.5 }",
		"a { n := n + 1 } { m := n - m } { r := r * 2.0 } { q := r / q } { r := ABS(q) }",
		"a { n := (n + 3) * m - 7 } { r := SQRT(q) + ABS(r) ** 2.0 } { n := n MOD 4 } { r := TRUNC(q) }",
		"GT(n + 1, m) LE(r * 2.0, q) EQ(n MOD 2, 0) ( b )",
		"a [ { n := 1 } | b { n := 2 } ] ( b )",
		"a { n := 0 } { r := 0.0 } { n := -m } { q := r ** 2.0 }",
		"GE(n, 0) LE(n, 100) ( b )",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, _ := l5x.Parse(out)
	got := rungTexts(back, "Main", "MainRoutine")
	want := []string{
		"XIC(a)MOVE(m,n)MOVE(1.5,r);",
		"XIC(a)ADD(n,1,n)SUB(n,m,m)MUL(r,2.0,r)DIV(r,q,q)ABS(q,r);",
		"XIC(a)CPT(n,((n + 3) * m) - 7)CPT(r,SQR(q) + (ABS(r) ** 2.0))CPT(n,n MOD 4)CPT(r,TRN(q));",
		"CMP(n + 1 > m)CMP(r * 2.0 <= q)CMP(n MOD 2 = 0)OTE(b);",
		"XIC(a)[MOVE(1,n) ,XIC(b) MOVE(2,n) ]OTE(b);",
		"XIC(a)MOVE(0,n)MOVE(0.0,r)CPT(n,-m)CPT(q,r ** 2.0);",
		"GE(n,0)LE(n,100)OTE(b);",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("written back:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	tags2 := string(p.Files["tags/logix.yaml"])
	if !strings.Contains(tags2, "name: n, role: output") {
		t.Errorf("n is assigned; should be an output:\n%s", tags2)
	}
}

// Arrays of timers and counters: the array is declared once, each
// element is its own instance with its own preset, and members read by
// index. The miniL5X helper carries one preset per tag as a scalar
// structure, so every element here presets to 1000 (a real export carries
// one per element; TestArrayOfStructuresDecodes in lang/l5x covers that).
func TestArraysOfBlocksImport(t *testing.T) {
	tags := []string{"a:BOOL", "b:BOOL", "c:BOOL", "i:DINT", "Timers:TIMER=1000", "Counts:COUNTER=3"}
	doc := miniL5X(tags,
		"MOVE(500,Timers[0].PRE);",
		"XIC(a)TON(Timers[0],?,?);",
		"XIC(Timers[0].DN)OTE(b);",
		"MOVE(1000,Timers[i].PRE);",
		"XIC(a)TON(Timers[i],?,?);",
		"XIC(a)CTU(Counts[1],?,?);",
		"XIC(Counts[1].DN)OTE(c);",
	)
	doc = []byte(strings.Replace(string(doc), `<Tag Name="Timers" TagType="Base" DataType="TIMER"`, `<Tag Name="Timers" TagType="Base" DataType="TIMER" Dimensions="3"`, 1))
	doc = []byte(strings.Replace(string(doc), `<Tag Name="Counts" TagType="Base" DataType="COUNTER"`, `<Tag Name="Counts" TagType="Base" DataType="COUNTER" Dimensions="2"`, 1))
	p := mustImport(t, doc)
	for _, nt := range p.Notes {
		if nt.Rung >= 0 {
			t.Errorf("note: %s", nt)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"Timers : ARRAY [0..2] OF TON;",
		"Counts : ARRAY [0..1] OF CTU;",
		"a Timers[0]:TON(PT := T#500MS)",
		"Timers[0].Q ( b )",
		"a Timers[i]:TON(PT := T#1S)",
		"a Counts[1]:CTU(PV := 3) ( c )",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, _ := l5x.Parse(out)
	got := rungTexts(back, "Main", "MainRoutine")
	want := []string{
		"MOVE(500,Timers[0].PRE);",
		"XIC(a)TON(Timers[0],?,?);",
		"XIC(Timers[0].DN)OTE(b);",
		"MOVE(1000,Timers[i].PRE);",
		"XIC(a)TON(Timers[i],?,?);",
		"XIC(a)CTU(Counts[1],?,?);",
		"XIC(Counts[1].DN)OTE(c);",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("written back:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A RES on a timer the routine runs with TON is the logix dialect's TONR
// (Reset := the RES rung's condition), and the manifest opts into the
// dialect; a rack point named directly becomes an alias tag.
func TestDialectAndAliasImport(t *testing.T) {
	tags := []string{"Go:BOOL", "Pulse:BOOL", "t:TIMER=1000", "Clear:BOOL"}
	doc := miniL5X(tags,
		"XIC(t.DN)RES(t);",
		"XIC(Go)TON(t,?,?)XIC(t.DN)OTE(Pulse);",
		"XIC(Local:1:I.Data.3)XIO(Clear)OTE(Local:2:O.Data.0);",
	)
	p := mustImport(t, doc)
	for _, nt := range p.Notes {
		if nt.Rung >= 0 {
			t.Errorf("note: %s", nt)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"t : TONR;",
		"Go t:TONR(PT := T#1S, Reset := t.Q) ( Pulse )",
		"Local_1_I_Data_3 /Clear ( Local_2_O_Data_0 )",
		"Local_1_I_Data_3 : BOOL;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	man := string(p.Files["nautilus.yaml"])
	if !strings.Contains(man, "dialect: logix") {
		t.Errorf("manifest should opt into the dialect:\n%s", man)
	}
	tf := string(p.Files["tags/logix.yaml"])
	for _, want := range []string{
		`name: Local_1_I_Data_3, role: input, alias: "Local:1:I.Data.3"`,
		`name: Local_2_O_Data_0, role: output, alias: "Local:2:O.Data.0"`,
	} {
		if !strings.Contains(tf, want) {
			t.Errorf("missing %q in\n%s", want, tf)
		}
	}
	// Written back with the dialect library in scope: the RES rung ahead
	// of the timer, the alias tags as alias tags.
	libs, _ := dialect.Sources("logix")
	var libSrc []string
	for _, l := range libs {
		libSrc = append(libSrc, l.ST)
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini", Libs: libSrc, Aliases: map[string]string{"Local_1_I_Data_3": "Local:1:I.Data.3", "Local_2_O_Data_0": "Local:2:O.Data.0"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, _ := l5x.Parse(out)
	got := rungTexts(back, "Main", "MainRoutine")
	want := []string{
		"XIC(t.DN)RES(t);",
		"XIC(Go)TON(t,?,?)XIC(t.DN)OTE(Pulse);",
		"XIC(Local_1_I_Data_3)XIO(Clear)OTE(Local_2_O_Data_0);",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("written back:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !strings.Contains(string(out), `TagType="Alias" Radix="Decimal" AliasFor="Local:1:I.Data.3"`) {
		t.Errorf("alias tag not written back")
	}
}

// The implicit-st golden (#248) imports with every controller tag the
// program named without declaring it declared VAR_EXTERNAL, typed as
// written (the enumeration as its DINT), and — minus the timer, whose
// Logix ST form the import keeps verbatim (TONR) — writes back to the
// same controller tags and the same ST routine.
func TestImplicitSTGoldenImportsAndWritesBack(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "implicit-st.golden.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	orig, err := l5x.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	tagSet := func(f *l5x.File) string {
		var s []string
		for _, tg := range f.Controller.Tags {
			s = append(s, tg.Name+":"+tg.DataType)
		}
		sort.Strings(s)
		return strings.Join(s, " ")
	}
	p := mustImport(t, doc)
	src := string(p.Files["ImplicitST.st"])
	var declared []string
	for _, tg := range orig.Controller.Tags {
		declared = append(declared, tg.Name+" : "+tg.DataType+";")
	}
	for _, d := range declared {
		if !strings.Contains(src, d) {
			t.Errorf("import does not declare %q:\n%s", d, src)
		}
	}

	// Write-back identity, without the timer.
	main, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "conformance", "implicit-st", "Main.st"))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "conformance", "implicit-st", "lib", "types.st"))
	if err != nil {
		t.Fatal(err)
	}
	noTimer := strings.NewReplacer("    t : TON;\n", "", "t(IN := Run, PT := T#2S, Q => Delayed);\n", "").Replace(string(main))
	tags := map[string]string{"Level": "REAL", "HiSP": "REAL", "Cmd": "DINT", "HiAlm": "BOOL", "Count": "DINT",
		"Pump": "PumpData", "State": "MachineState", "StateNo": "DINT", "Speed": "REAL"}
	first, diags, err := writer.WriteST(noTimer, writer.Options{Controller: "DemoLine", Libs: []string{string(lib)}, Tags: tags})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	f1, err := l5x.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	p = mustImport(t, first)
	out, diags, err := writer.WriteST(string(p.Files["ImplicitST.st"]), writer.Options{Controller: "DemoLine", Libs: libsOf(p)})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writing the import back: %v %v\n%s", err, diags, p.Files["ImplicitST.st"])
	}
	back, err := l5x.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tagSet(back), tagSet(f1); got != want || !strings.Contains(want, "State:DINT") || !strings.Contains(want, "Pump:PumpData") {
		t.Errorf("controller tags after import+write\n got: %s\nwant: %s", got, want)
	}
	routine := func(f *l5x.File) string {
		for _, pr := range f.Controller.Programs {
			if pr.Name == "ImplicitST" {
				return pr.Routines[0].Text
			}
		}
		return ""
	}
	if got, want := routine(back), routine(f1); got != want || want == "" {
		t.Errorf("ST routine after import+write differs\n got:\n%s\nwant:\n%s", got, want)
	}
}
