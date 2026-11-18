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
<DataTypes>
<DataType Name="Hist" Family="NoFamily" Class="User">
<Members>
<Member Name="Vals" DataType="DINT" Dimension="3" Radix="Decimal" Hidden="false" ExternalAccess="Read/Write"/>
<Member Name="Lvls" DataType="REAL" Dimension="2" Radix="Float" Hidden="false" ExternalAccess="Read/Write"/>
</Members>
</DataType>
</DataTypes>
<Tags>
<Tag Name="Flags" TagType="Base" DataType="BOOL" Dimensions="32" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="L5K"><![CDATA[[8]]]></Data>
<Data Format="Decorated">
<Array DataType="BOOL" Dimensions="32" Radix="Decimal">
<Element Index="[0]" Value="0"/>
<Element Index="[1]" Value="0"/>
<Element Index="[2]" Value="0"/>
<Element Index="[3]" Value="1"/>
</Array>
</Data>
</Tag>
<Tag Name="H" TagType="Base" DataType="Hist" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated">
<Structure DataType="Hist">
<ArrayMember Name="Vals" DataType="DINT" Dimensions="3" Radix="Decimal">
<Element Index="[0]" Value="11"/>
<Element Index="[1]" Value="0"/>
<Element Index="[2]" Value="0"/>
</ArrayMember>
<ArrayMember Name="Lvls" DataType="REAL" Dimensions="2" Radix="Float">
<Element Index="[0]" Value="0.0"/>
<Element Index="[1]" Value="2.5"/>
</ArrayMember>
</Structure>
</Data>
</Tag>
<Tag Name="HA" TagType="Base" DataType="Hist" Dimensions="2" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated">
<Array DataType="Hist" Dimensions="2">
<Element Index="[0]"><Structure DataType="Hist"><ArrayMember Name="Vals" DataType="DINT" Dimensions="3" Radix="Decimal"><Element Index="[0]" Value="4"/><Element Index="[1]" Value="0"/><Element Index="[2]" Value="0"/></ArrayMember></Structure></Element>
<Element Index="[1]"><Structure DataType="Hist"><ArrayMember Name="Vals" DataType="DINT" Dimensions="3" Radix="Decimal"><Element Index="[0]" Value="0"/><Element Index="[1]" Value="0"/><Element Index="[2]" Value="0"/></ArrayMember></Structure></Element>
</Array>
</Data>
</Tag>
<Tag Name="Bit" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="HV" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="LV" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="HL" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
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
<Tags>
<Tag Name="Loc" TagType="Base" DataType="DINT" Dimensions="2" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Array DataType="DINT" Dimensions="2" Radix="Decimal"><Element Index="[0]" Value="9"/><Element Index="[1]" Value="0"/></Array></Data>
</Tag>
</Tags>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[MOV(Tbl[2],Out);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[MOV(Zeros[1],Gain);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[XIC(Flags[3])OTE(Bit);]]></Text></Rung>
<Rung Number="3" Type="N"><Text><![CDATA[MOV(H.Vals[0],HV);]]></Text></Rung>
<Rung Number="4" Type="N"><Text><![CDATA[MOV(H.Lvls[1],HL);]]></Text></Rung>
<Rung Number="5" Type="N"><Text><![CDATA[MOV(Loc[1],LV);]]></Text></Rung>
<Rung Number="6" Type="N"><Text><![CDATA[OTE(Lamp);]]></Text></Rung>
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
	out, code := capture(t, func() int { return runLogix([]string{"import", "--project", proj, src}) })
	if code != 0 {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	// What still starts at zero says so: an array of structures, and a
	// program's own array (a local, so it exists, but not at its values).
	for _, want := range []string{
		"HA: starts at zero in nautilus; the export's values for it (Hist[2]) are not carried",
		"Loc: a program's array starts at zero in nautilus; the export's values for it are not carried",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the import does not note %q:\n%s", want, out)
		}
	}
	tags, err := os.ReadFile(filepath.Join(proj, "tags", "logix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`- { name: Tbl, role: input, type: "ARRAY [0..3] OF DINT", init: [0, 5, 7] }`,
		`- { name: Zeros, role: input, type: "ARRAY [0..2] OF REAL", init: [] }`,
		// A BOOL array's elements are BOOLs, from the <Array>'s type.
		`- { name: Flags, role: input, type: "ARRAY [0..31] OF BOOL", init: [false, false, false, true] }`,
		// A structure's member arrays are carried.
		`- { name: H, role: input, type: Hist, init: { Lvls: [0.0, 2.5], Vals: [11] } }`,
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
        expect: { Out: 7, LV: 0, Gain: 0.0, Bit: true, HV: 11, HL: 2.5, Lamp: true }
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
	flags := make([]any, 32)
	for i := range flags {
		flags[i] = i == 3
	}
	want := map[string]any{
		"Tbl":   []any{int64(0), int64(5), int64(7), int64(0)},
		"Zeros": []any{0.0, 0.0, 0.0},
		"Flags": flags,
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
