package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// arrayL5X is a synthetic export: an array controller tag no rung writes,
// with the values the export carries for it, read by one rung, and an
// unconditional coil after it. (Logix stores those values in the project,
// so the controller runs from them.)
const arrayL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
<Tag Name="Tbl" TagType="Base" DataType="DINT" Dimensions="4" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated">
<Array DataType="DINT" Dimensions="4" Radix="Decimal">
<Element Index="[0]" Value="0"/>
<Element Index="[1]" Value="5"/>
<Element Index="[2]" Value="7"/>
<Element Index="[3]" Value="0"/>
</Array>
</Data>
</Tag>
<Tag Name="Zeros" TagType="Base" DataType="REAL" Dimensions="3" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated">
<Array DataType="REAL" Dimensions="3" Radix="Float">
<Element Index="[0]" Value="0.0"/>
<Element Index="[1]" Value="0.0"/>
<Element Index="[2]" Value="0.0"/>
</Array>
</Data>
</Tag>
<Tag Name="Out" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Gain" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="Lamp" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
</Tags>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[MOV(Tbl[2],Out);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[MOV(Zeros[1],Gain);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[OTE(Lamp);]]></Text></Rung>
</RLLContent>
</Routine>
</Routines>
</Program>
</Programs>
<Tasks>
<Task Name="MainTask" Type="PERIODIC" Rate="10" Priority="10" Watchdog="500">
<ScheduledPrograms><ScheduledProgram Name="Main"/></ScheduledPrograms>
</Task>
</Tasks>
</Controller>
</RSLogix5000Content>
`

// #774: an imported array tag the logic only reads is a typed input no
// driver delivers, so without an init: it was never seeded, and the rung
// reading it faulted the scan ("undefined tag Tbl") on every scan, leaving
// the coil after it dark. The import now seeds it from the export.
func TestLogixImportSeedsArraysFromTheExport(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(arrayL5X), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "mini")
	if out, code := capture(t, func() int { return runLogix([]string{"import", "--project", proj, src}) }); code != 0 {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	tags, err := os.ReadFile(filepath.Join(proj, "tags", "logix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`- { name: Tbl, role: input, type: "ARRAY [0..3] OF DINT", init: [0, 5, 7] }`,
		`- { name: Zeros, role: input, type: "ARRAY [0..2] OF REAL", init: [] }`,
	} {
		if !strings.Contains(string(tags), want) {
			t.Errorf("tag file lacks %s:\n%s", want, tags)
		}
	}

	// One scan, as `naut test` runs it: the read lands, and the coil after
	// it is energized.
	if err := os.WriteFile(filepath.Join(proj, "seed_test.yaml"), []byte(`tests:
  - name: an imported array is there on the first scan
    steps:
      - scans: 1
        expect: { Out: 7, Gain: 0.0, Lamp: true }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int { return runTest([]string{proj}) }); code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("naut test on the imported project: exit %d\n%s", code, out)
	}

	// Written back, the arrays carry the same values.
	back := filepath.Join(dir, "back.L5X")
	if out, code := capture(t, func() int {
		return runLogix([]string{"write", "-o", back, filepath.Join(proj, "Main.ld")})
	}); code != 0 {
		t.Fatalf("write: exit %d\n%s", code, out)
	}
	doc, err := l5x.ParseFile(back)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Tbl":   []any{int64(0), int64(5), int64(7), int64(0)},
		"Zeros": []any{0.0, 0.0, 0.0},
	}
	for _, tg := range doc.Controller.Tags {
		if w, ok := want[tg.Name]; ok {
			if !reflect.DeepEqual(tg.Value, w) {
				t.Errorf("written back, %s = %#v, want %#v", tg.Name, tg.Value, w)
			}
			delete(want, tg.Name)
		}
	}
	if len(want) > 0 {
		t.Errorf("not written back: %v", want)
	}
}
