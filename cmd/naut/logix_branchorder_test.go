package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// branchOrderL5X is a synthetic export: a branch whose first leg copies a
// counter's count and whose last leg counts (Logix runs the legs top to
// bottom, so the copy reads the count before the counter adds one), and a
// rung where a box on an earlier leg writes what a later counter's leg
// compares.
const branchOrderL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
<Tag Name="go" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="1"/></Data>
</Tag>
<Tag Name="Cnt" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Lim" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="C1" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="100"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="C2" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="100"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
</Tags>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[XIC(go)[MOV(C1.ACC,Cnt) ,ONS(Os)CTU(C1,?,?) ];]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[XIC(go)[MOV(5,Lim) ,GRT(Lim,3)CTU(C2,?,?) ];]]></Text></Rung>
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

// #778: a counter on a later branch leg was hoisted to a rung ahead of the
// earlier legs' data boxes, so a box copying its count read it after the
// count. It runs after them now. And where a box on an earlier leg writes
// what the counter's own leg tests, either order would read the condition
// at the wrong time, so that rung is refused with the reason.
func TestLogixImportKeepsBranchLegOrder(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(branchOrderL5X), 0o644); err != nil {
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
	copyAt := strings.Index(ld, "{ Cnt := C1.CV }")
	countAt := strings.Index(ld, "C1:CTU(")
	if copyAt < 0 || countAt < 0 || copyAt > countAt {
		t.Errorf("the copy of the count does not come before the counter:\n%s", ld)
	}
	if !strings.Contains(ld, "// not imported (branch-order: a data box on an earlier leg writes Lim, which a condition of the timer or counter on a later leg reads;") {
		t.Errorf("the rung whose box writes the counter's condition is not refused:\n%s", ld)
	}

	// The controller's values: the first scan copies 0 and counts to 1;
	// the next copies 1 (the one-shot does not count again).
	if err := os.WriteFile(filepath.Join(proj, "order_test.yaml"), []byte(`tests:
  - name: the copy reads the count before the counter runs
    steps:
      - scans: 1
        expect: { Cnt: 0 }
      - scans: 1
        expect: { Cnt: 1 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int { return runTest([]string{proj}) }); code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("naut test: exit %d\n%s\n%s", code, out, ld)
	}
}
