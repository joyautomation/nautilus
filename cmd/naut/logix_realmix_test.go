package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realMixL5X is a synthetic export: boxes that mix a DINT with a REAL into
// a REAL, which Logix computes in REAL, and a DINT moved into a REAL.
const realMixL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
<Tag Name="D" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="7"/></Data>
</Tag>
<Tag Name="R" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.5"/></Data>
</Tag>
<Tag Name="A1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="M1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="V1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="C1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="W1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
</Tags>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[ADD(D,R,A1);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[MUL(D,0.5,M1);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[DIV(D,2.0,V1);]]></Text></Rung>
<Rung Number="3" Type="N"><Text><![CDATA[CPT(C1,D*R+1);]]></Text></Rung>
<Rung Number="4" Type="N"><Text><![CDATA[MOV(D,W1);]]></Text></Rung>
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

// #782: IEC's table does not take a DINT into a REAL implicitly, so the
// import writes the conversion Logix makes as it computes a mixed box in
// REAL (DINT_TO_REAL on the DINT operand, or on the whole value of a MOV),
// the project checks for both targets and runs to Logix's values, and the
// writer folds every conversion back into the box it came from.
func TestLogixImportDintBesideARealConverts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(realMixL5X), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "mini")
	if out, code := capture(t, func() int { return runLogix([]string{"import", "--project", proj, src}) }); code != 0 {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	body, err := os.ReadFile(filepath.Join(proj, "Main.ld"))
	if err != nil {
		t.Fatal(err)
	}
	ld := string(body)
	for _, want := range []string{
		"{ A1 := DINT_TO_REAL(D) + R }",
		"{ M1 := DINT_TO_REAL(D) * 0.5 }",
		"{ V1 := DINT_TO_REAL(D) / 2.0 }",
		"{ C1 := DINT_TO_REAL(D) * R + 1 }",
		"{ W1 := DINT_TO_REAL(D) }",
	} {
		if !strings.Contains(ld, want) {
			t.Errorf("Main.ld lacks %q:\n%s", want, ld)
		}
	}
	for _, args := range [][]string{{proj}, {"--target", "logix", proj}} {
		if out, code := capture(t, func() int { return runCheck(args) }); code != 0 {
			t.Errorf("naut check %v: exit %d\n%s", args, code, out)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, "mix_test.yaml"), []byte(`tests:
  - name: a DINT beside a REAL is computed in REAL
    steps:
      - scans: 1
        expect: { A1: 7.5, M1: 3.5, V1: 3.5, C1: 4.5, W1: 7.0 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int { return runTest([]string{proj}) }); code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("naut test: exit %d\n%s", code, out)
	}
	back := filepath.Join(dir, "back.L5X")
	if out, code := capture(t, func() int {
		return runLogix([]string{"write", "-o", back, filepath.Join(proj, "Main.ld")})
	}); code != 0 {
		t.Fatalf("write: exit %d\n%s", code, out)
	}
	written, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ADD(D,R,A1);", "MUL(D,0.5,M1);", "DIV(D,2.0,V1);", "MOVE(D,W1);"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("written back without %q", want)
		}
	}
	if !strings.Contains(string(written), "CPT(C1,") || strings.Contains(string(written), "DINT_TO_REAL") {
		t.Errorf("the CPT is not written back as a CPT of D*R+1")
	}
}
