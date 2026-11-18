package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// branchOrderL5X is a synthetic export of branches Logix runs leg by leg,
// top to bottom: a copy of a counter's count ahead of the count; a box
// that sets what a later leg's compare reads (the compare sees it); boxes
// on both sides of a counter; a leg that latches what the rung's own
// condition reads; and a one-shot leg ahead of a counter.
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
<Tag Name="Cnt2" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Lim2" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="A" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="B" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Y" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Run" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="C3" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="100"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="C4" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="100"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="C5" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="100"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="St" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Run2" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Stp" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="T9" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="1000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
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
<Rung Number="2" Type="N"><Text><![CDATA[XIC(go)MOV(C2.ACC,Cnt2);]]></Text></Rung>
<Rung Number="3" Type="N"><Text><![CDATA[XIC(go)[MOV(C3.ACC,A) ,CTU(C3,?,?) ,MOV(C3.ACC,B) ];]]></Text></Rung>
<Rung Number="4" Type="N"><Text><![CDATA[XIC(go)XIO(Run)[MOV(5,Lim2) OTL(Run) ,CTU(C4,?,?) ];]]></Text></Rung>
<Rung Number="5" Type="N"><Text><![CDATA[XIC(go)[ONS(Os5)MOV(1,Y) ,CTU(C5,?,?) ];]]></Text></Rung>
<Rung Number="6" Type="N"><Text><![CDATA[[XIC(St) ,XIC(Run2) ]XIO(Stp)[OTE(Run2) ,TON(T9,?,?) ];]]></Text></Rung>
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
// count. It runs after them now (its one-shot's pulse still ahead, since
// a leg reads it), and a later leg's compare sees what an earlier box
// set, as in Logix. What no order of rungs can do is refused with its
// reason: boxes on both sides of a block, and a leg writing what the
// rung's condition ahead of the branch reads.
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
	for _, want := range []string{
		"// not imported (branch-order: data boxes both before and after a timer, counter or message in one branch",
		"// not imported (branch-order: a leg writes Run, which the rung's condition ahead of the branch reads",
		"{ Lim := 5 }",
		// A seal-in beside a timer re-reads its own coil harmlessly (the
		// condition is monotone in it): carried, not refused.
		"T9:TON(",
	} {
		if !strings.Contains(ld, want) {
			t.Errorf("Main.ld lacks %q:\n%s", want, ld)
		}
	}

	// The controller's values: the first scan copies 0 and counts to 1;
	// the next copies 1 (the one-shot does not count again).
	if err := os.WriteFile(filepath.Join(proj, "order_test.yaml"), []byte(`tests:
  - name: the copy reads the count before the counter runs
    steps:
      - scans: 1
        expect: { Cnt: 0, Cnt2: 1, Y: 1 }
      - scans: 1
        expect: { Cnt: 1, Cnt2: 1 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int { return runTest([]string{proj}) }); code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("naut test: exit %d\n%s\n%s", code, out, ld)
	}
}
