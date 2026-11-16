package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/writer"
)

// testdata/running.L5X is a real upload of the keep.ld project from an
// Echo controller (v38), converted to L5X by the SDK, after values were
// written over EtherNet/IP: HiLevelSP 72.5, SpareSP 42, Pump1.Speed 55.5,
// Pump1.Status.Mode 3, MainProgram.LevelLog[2] 12.25, MainProgram.Starts
// accumulator 17. Its project initial values are 85.0, 5, 40.0, 0, 0, 0.

func keepOptions(t *testing.T) writer.Options {
	t.Helper()
	types, err := os.ReadFile(filepath.Join("testdata", "keep_types.st"))
	if err != nil {
		t.Fatal(err)
	}
	return writer.Options{
		Controller: "DemoLine", PeriodMs: 100, Libs: []string{string(types)},
		Inits: map[string]any{
			"StartPB": false, "StopPB": false, "LevelPct": 0.0, "HiLevelSP": 85.0,
			"RunCmd": false, "HiLevelAlm": false, "Pump1": map[string]any{"Speed": 40.0}, "SpareSP": 5,
		},
		Tags: map[string]string{
			"StartPB": "BOOL", "StopPB": "BOOL", "LevelPct": "REAL", "HiLevelSP": "REAL",
			"RunCmd": "BOOL", "HiLevelAlm": "BOOL", "Pump1": "Pump", "SpareSP": "DINT",
		},
		Descs: map[string]string{"HiLevelSP": "LAH-101 setpoint"},
		Side:  writer.Side{Heartbeat: "Nautilus_Scan"},
	}
}

func keepSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "keep.ld"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func running(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "running.L5X"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// plan writes src as a project and plans the running controller's values
// into it, returning the plan and the project with the values applied,
// parsed.
func plan(t *testing.T, src string, o writer.Options) (*ValuePlan, *l5x.File, []byte) {
	t.Helper()
	full, diags, err := writer.Write(src, o)
	if err != nil || len(diags) > 0 {
		t.Fatalf("write: %v %v", err, diags)
	}
	gen, err := l5x.Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanValues(gen, running(t))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := p.Apply(full)
	if err != nil {
		t.Fatal(err)
	}
	f, err := l5x.Parse(merged)
	if err != nil {
		t.Fatalf("the merged project does not parse: %v\n%s", err, merged)
	}
	return p, f, merged
}

func tagIn(t *testing.T, f *l5x.File, scope, name string) *l5x.Tag {
	t.Helper()
	for _, s := range scopedTags(f) {
		if s.key == tagKey(scope, name) {
			return s.tag
		}
	}
	t.Fatalf("no tag %s/%s", scope, name)
	return nil
}

func TestKeepValuesCarriesEveryKindOfValue(t *testing.T) {
	p, f, _ := plan(t, keepSource(t), keepOptions(t))
	if len(p.Reset) != 0 || len(p.New) != 0 || len(p.Dropped) != 0 {
		t.Errorf("plan = %s", p.Summary())
	}
	if len(p.Kept) != 12 {
		t.Errorf("kept %d: %v", len(p.Kept), p.Kept)
	}
	if v := tagIn(t, f, "", "HiLevelSP").Value; v != 72.5 {
		t.Errorf("HiLevelSP = %v, want the controller's 72.5", v)
	}
	if v := tagIn(t, f, "", "SpareSP").Value; v != int64(42) {
		t.Errorf("SpareSP = %v (%T)", v, v)
	}
	pump := tagIn(t, f, "", "Pump1").Value.(map[string]any)
	if pump["Speed"] != 55.5 || pump["Status"].(map[string]any)["Mode"] != int64(3) {
		t.Errorf("Pump1 = %v", pump)
	}
	if v := tagIn(t, f, "MainProgram", "LevelLog").Value.([]any); v[2] != 12.25 {
		t.Errorf("LevelLog = %v", v)
	}
	starts := tagIn(t, f, "MainProgram", "Starts").Value.(map[string]any)
	if starts["ACC"] != int64(17) || starts["PRE"] != int64(1000) {
		t.Errorf("Starts = %v", starts)
	}
}

// The logic is untouched: the merge only replaces tag data.
func TestKeepValuesLeavesTheLogicAlone(t *testing.T) {
	full, _, _ := writer.Write(keepSource(t), keepOptions(t))
	gen, _ := l5x.Parse(full)
	_, f, merged := plan(t, keepSource(t), keepOptions(t))
	if d := l5x.LogicDiff(l5x.LogicOf(gen), l5x.LogicOf(f)); len(d) > 0 {
		t.Errorf("logic changed: %v", d)
	}
	// Descriptions survive (they are the project's, before the data).
	if !strings.Contains(string(merged), "LAH-101 setpoint") {
		t.Error("lost a description")
	}
	// The controller's copy goes in as the SDK wrote it, both formats.
	if !strings.Contains(string(merged), "<![CDATA[7.25000000e+001]]>") {
		t.Error("the L5K copy of HiLevelSP is not the controller's")
	}
}

// A preset written as a literal is code: a changed PT takes effect, the
// accumulator is still kept.
func TestKeepValuesTakesPresetsFromTheProject(t *testing.T) {
	src := strings.Replace(keepSource(t), "PV := 1000", "PV := 250", 1)
	src = strings.Replace(src, "PT := T#30S", "PT := T#45S", 1)
	p, f, merged := plan(t, src, keepOptions(t))
	starts := tagIn(t, f, "MainProgram", "Starts").Value.(map[string]any)
	if starts["ACC"] != int64(17) || starts["PRE"] != int64(250) {
		t.Errorf("Starts = %v, want ACC 17 kept and PRE 250 from the project", starts)
	}
	if tmr := tagIn(t, f, "MainProgram", "RunTmr").Value.(map[string]any); tmr["PRE"] != int64(45000) {
		t.Errorf("RunTmr = %v", tmr)
	}
	if !strings.Contains(string(merged), "<![CDATA[[0,250,17]]]>") {
		t.Error("the L5K copy of Starts should carry the project's preset and the controller's accumulator")
	}
	if len(p.Reset) != 0 {
		t.Errorf("reset = %v", p.Reset)
	}
}

// A changed type, a changed structure, a constant, a new tag and a removed
// one: each reported, none carried.
func TestKeepValuesResetsWhatChanged(t *testing.T) {
	o := keepOptions(t)
	o.Tags["SpareSP"] = "REAL"
	o.Inits["SpareSP"] = 5.0
	o.Tags["NewSP"] = "DINT"
	o.Inits["NewSP"] = 7
	o.Libs = []string{strings.Replace(o.Libs[0], "Alarm : BOOL;", "Alarm : BOOL;\n    Code : DINT;", 1)}
	src := keepSource(t)
	src = strings.Replace(src, "SpareSP    : DINT;", "SpareSP    : REAL;\n    NewSP      : DINT;", 1)
	src = strings.Replace(src, "GE(SpareSP, 0)", "GE(SpareSP, 0.0) GE(NewSP, 0)", 1)
	src = strings.Replace(src, "LevelLog : ARRAY [0..3] OF REAL;", "LevelLog : ARRAY [0..7] OF REAL;", 1)
	p, f, _ := plan(t, src, o)
	want := []string{
		"MainProgram.LevelLog: REAL[4] → REAL[8]",
		"Pump1: the definition of Pump changed",
		"SpareSP: DINT → REAL",
	}
	if strings.Join(p.Reset, "|") != strings.Join(want, "|") {
		t.Errorf("reset = %q\nwant    %q", p.Reset, want)
	}
	if len(p.New) != 1 || p.New[0] != "NewSP" {
		t.Errorf("new = %v", p.New)
	}
	if v := tagIn(t, f, "", "SpareSP").Value; v != 5.0 {
		t.Errorf("SpareSP = %v, want the project's 5.0", v)
	}
	if v := tagIn(t, f, "", "NewSP").Value; v != int64(7) {
		t.Errorf("NewSP = %v, want the project's 7", v)
	}
	if v := tagIn(t, f, "", "HiLevelSP").Value; v != 72.5 {
		t.Errorf("HiLevelSP = %v, still kept", v)
	}
	if !strings.Contains(p.Summary(), "3 reset to the project's value") {
		t.Errorf("summary = %s", p.Summary())
	}

	// A tag the project no longer has: the download removes it.
	src = strings.Replace(keepSource(t), "    LevelLog : ARRAY [0..3] OF REAL;\n", "", 1)
	src = strings.Replace(src, " GE(LevelLog[0], 0.0)", "", 1)
	p, _, _ = plan(t, src, keepOptions(t))
	if len(p.Dropped) != 1 || p.Dropped[0] != "MainProgram.LevelLog" {
		t.Errorf("dropped = %v", p.Dropped)
	}
}

// Names match whatever the case: Logix names are case-insensitive.
func TestKeepValuesMatchesNamesWithoutCase(t *testing.T) {
	run := strings.ReplaceAll(string(running(t)), `Tag Name="HiLevelSP"`, `Tag Name="HILEVELSP"`)
	full, _, _ := writer.Write(keepSource(t), keepOptions(t))
	gen, _ := l5x.Parse(full)
	p, err := PlanValues(gen, []byte(run))
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := p.Apply(full)
	f, _ := l5x.Parse(merged)
	if v := tagIn(t, f, "", "HiLevelSP").Value; v != 72.5 {
		t.Errorf("HiLevelSP = %v", v)
	}
}

// A tag with no data of its own in the project (an Add-On Instruction
// instance: the SDK fills it) takes the controller's data, and a
// self-closing one is opened for it.
func TestApplyInsertsDataWhereTheProjectHasNone(t *testing.T) {
	doc := []byte(`<RSLogix5000Content><Controller Name="C"><Tags>
<Tag Name="A" TagType="Base" DataType="M">
<Description>
<![CDATA[d]]>
</Description>
</Tag>
<Tag Name="B" TagType="Base" DataType="M"/>
</Tags><Programs/></Controller></RSLogix5000Content>`)
	p := &ValuePlan{data: map[string][]byte{
		"/a": []byte(`<Data Format="Decorated"><DataValue DataType="DINT" Value="1"/></Data>`),
		"/b": []byte(`<Data Format="Decorated"><DataValue DataType="DINT" Value="2"/></Data>`),
	}}
	out, err := p.Apply(doc)
	if err != nil {
		t.Fatal(err)
	}
	f, err := l5x.Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if f.Controller.Tags[0].Value != int64(1) || f.Controller.Tags[1].Value != int64(2) || f.Controller.Tags[0].Description != "d" {
		t.Errorf("tags = %+v %+v\n%s", f.Controller.Tags[0], f.Controller.Tags[1], out)
	}
}

// Only Controller/Tags and Program/Tags are tags with values; an Add-On
// Instruction's local tags are part of its definition.
func TestTagSpansSkipsAOILocals(t *testing.T) {
	doc := []byte("\xef\xbb\xbf" + `<?xml version="1.0"?><RSLogix5000Content><Controller Name="C">
<AddOnInstructionDefinitions><AddOnInstructionDefinition Name="M"><LocalTags><LocalTag Name="X" DataType="DINT"><DefaultData Format="Decorated"/></LocalTag></LocalTags></AddOnInstructionDefinition></AddOnInstructionDefinitions>
<Tags><Tag Name="T" DataType="DINT"><Data Format="L5K"><![CDATA[3]]></Data><Data Format="Decorated"><DataValue DataType="DINT" Value="3"/></Data></Tag></Tags>
<Programs><Program Name="P"><Tags><Tag Name="U" DataType="BOOL"><Data Format="Decorated"><DataValue DataType="BOOL" Value="1"/></Data></Tag></Tags></Program></Programs>
</Controller></RSLogix5000Content>`)
	spans, err := tagSpans(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].key != "/t" || spans[1].key != "p/u" {
		t.Fatalf("spans = %+v", spans)
	}
	if len(spans[0].data) != 2 || spans[0].data[0].format != "L5K" || string(spans[0].data[0].raw) != `<Data Format="L5K"><![CDATA[3]]></Data>` {
		t.Errorf("T data = %+v", spans[0].data)
	}
}

// A MESSAGE tag's data is its MSG's parameters: a redeploy keeping values
// takes the project's, so a changed channel or element is not undone by
// the controller's old copy.
func TestKeepValuesTakesMessagesFromTheProject(t *testing.T) {
	src := "PROGRAM Main\nVAR_EXTERNAL Go : BOOL; X : DINT; END_VAR\nVAR\n  rd : READ;\nEND_VAR\nLD\n  RUNG r0\n    Go rd:READ(ID := 'line2', VAR_1 := 'Remote', RD_1 := X)\nEND_LD\nEND_PROGRAM\n"
	write := func(host string) []byte {
		out, diags, err := writer.Write(src, writer.Options{Channels: []writer.Channel{{Name: "line2", Type: "cip", Host: host}}})
		if err != nil || len(diags) > 0 {
			t.Fatalf("write: %v %v", err, diags)
		}
		return out
	}
	old := write("192.0.2.1")
	full := write("192.0.2.2")
	gen, err := l5x.Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanValues(gen, old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Reset, "|"), "rd: a message takes the project's parameters") {
		t.Errorf("reset = %q", p.Reset)
	}
	merged, err := p.Apply(full)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), `ConnectionPath="2, 192.0.2.2"`) || strings.Contains(string(merged), "192.0.2.1") {
		t.Errorf("merged project keeps the controller's path:\n%s", merged)
	}
}
