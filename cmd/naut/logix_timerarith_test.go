package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// timerArithL5X is a synthetic export: a running timer whose ACC and PRE,
// DINT milliseconds in Logix, feed arithmetic boxes and a CPT, and a timer
// whose ACC and PRE are moved into.
const timerArithL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
<Tag Name="go" TagType="Base" DataType="BOOL" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="BOOL" Radix="Decimal" Value="1"/></Data>
</Tag>
<Tag Name="Remain" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="RemI" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Half" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Secs" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="0.0"/></Data>
</Tag>
<Tag Name="T1" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="5000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="Ms" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="750"/></Data>
</Tag>
<Tag Name="Echo" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Echo2" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="T2" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="1000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="Echo3" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Echo4" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Echo5" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Echo6" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Neg" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="D4" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="T3" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="1000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="T4" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="1000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
</Tags>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[XIC(go)TON(T1,?,?);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[SUB(T1.PRE,T1.ACC,Remain);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[SUB(T1.PRE,T1.ACC,RemI);]]></Text></Rung>
<Rung Number="3" Type="N"><Text><![CDATA[MUL(T1.ACC,0.001,Secs);]]></Text></Rung>
<Rung Number="4" Type="N"><Text><![CDATA[CPT(Half,T1.ACC/2);]]></Text></Rung>
<Rung Number="5" Type="N"><Text><![CDATA[MOV(Ms,T2.ACC);]]></Text></Rung>
<Rung Number="6" Type="N"><Text><![CDATA[MOV(T2.ACC,Echo);]]></Text></Rung>
<Rung Number="7" Type="N"><Text><![CDATA[MOV(250,T2.PRE);]]></Text></Rung>
<Rung Number="8" Type="N"><Text><![CDATA[MOV(T2.PRE,Echo2);]]></Text></Rung>
<Rung Number="9" Type="N"><Text><![CDATA[ADD(T2.ACC,100,T2.ACC);]]></Text></Rung>
<Rung Number="10" Type="N"><Text><![CDATA[MOV(T2.ACC,Echo3);]]></Text></Rung>
<Rung Number="11" Type="N"><Text><![CDATA[MOV(16#EE,T2.PRE);]]></Text></Rung>
<Rung Number="12" Type="N"><Text><![CDATA[MOV(T2.PRE,Echo4);]]></Text></Rung>
<Rung Number="13" Type="N"><Text><![CDATA[CPT(T2.PRE,Ms*2);]]></Text></Rung>
<Rung Number="14" Type="N"><Text><![CDATA[MOV(T2.PRE,Echo5);]]></Text></Rung>
<Rung Number="15" Type="N"><Text><![CDATA[CPT(T2.PRE,T1.ACC);]]></Text></Rung>
<Rung Number="16" Type="N"><Text><![CDATA[MOV(T2.PRE,Echo6);]]></Text></Rung>
<Rung Number="17" Type="N"><Text><![CDATA[NEG(T1.ACC,Neg);]]></Text></Rung>
<Rung Number="18" Type="N"><Text><![CDATA[MOV(Ms,T1.ACC);]]></Text></Rung>
<Rung Number="19" Type="N"><Text><![CDATA[XIC(go)MUL(Ms,2,T4.PRE)TON(T4,?,?);]]></Text></Rung>
<Rung Number="20" Type="N"><Text><![CDATA[MOV(T4.PRE,D4);]]></Text></Rung>
<Rung Number="22" Type="N"><Text><![CDATA[XIC(go)TON(T3,?,?);]]></Text></Rung>
</RLLContent>
</Routine>
</Routines>
</Program>
<Program Name="Other" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[MOV(Ms,T3.PRE);]]></Text></Rung>
</RLLContent>
</Routine>
</Routines>
</Program>
</Programs>
<Tasks>
<Task Name="MainTask" Type="PERIODIC" Rate="10" Priority="10" Watchdog="500">
<ScheduledPrograms><ScheduledProgram Name="Main"/><ScheduledProgram Name="Other"/></ScheduledPrograms>
</Task>
</Tasks>
</Controller>
</RSLogix5000Content>
`

// #793: a timer's ACC or PRE in arithmetic is TIME in nautilus (ET, PT),
// which does not mix with an integer. It imports as TIME_TO_DINT(t.ET),
// the milliseconds Logix computes with; an integer moved into one goes in
// through DINT_TO_TIME. Both check for both targets, run to the
// controller's values, and write back as the box they were.
func TestLogixImportTimerMembersInArithmetic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(timerArithL5X), 0o644); err != nil {
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
		"{ Remain := TIME_TO_DINT(T1.PT) - TIME_TO_DINT(T1.ET) }",
		"{ RemI := DINT_TO_INT(TIME_TO_DINT(T1.PT) - TIME_TO_DINT(T1.ET)) }",
		"{ Secs := TIME_TO_DINT(T1.ET) * 0.001 }",
		"{ Half := TIME_TO_DINT(T1.ET) / 2 }",
		// Milliseconds into a timer's ACC or PRE (#793, the mirror case).
		"{ T2.ET := DINT_TO_TIME(Ms) }",
		"{ T2.PT := DINT_TO_TIME(250) }",
		"{ T2.ET := DINT_TO_TIME(TIME_TO_DINT(T2.ET) + 100) }",
		"{ T2.PT := DINT_TO_TIME(16#EE) }",
		"{ T2.PT := DINT_TO_TIME(Ms * 2) }",
		"{ T2.PT := DINT_TO_TIME(TIME_TO_DINT(T1.ET)) }",
		"{ Neg := -TIME_TO_DINT(T1.ET) }",
		// A preset the logic writes goes through its DINT preset variable.
		"{ D4 := T4_PRE }",
		// A write to a timer a TON runs would be lost: refused.
		"// not imported (timer: T1.ACC: written while a TON/TOF runs T1",
	} {
		if !strings.Contains(ld, want) {
			t.Errorf("Main.ld lacks %q:\n%s", want, ld)
		}
	}
	other, err := os.ReadFile(filepath.Join(proj, "Other.ld"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(other), "// not imported") || !strings.Contains(string(other), "MOV(Ms,T3.PRE)") {
		t.Errorf("a write to a timer another program runs is not refused:\n%s", other)
	}
	for _, args := range [][]string{{proj}, {"--target", "logix", proj}} {
		if out, code := capture(t, func() int { return runCheck(args) }); code != 0 {
			t.Errorf("naut check %v: exit %d\n%s", args, code, out)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, "arith_test.yaml"), []byte(`tests:
  - name: the timer's milliseconds, as the controller computes with them
    steps:
      - advance: 1s
        expect: { Remain: { near: 4000, tol: 10 }, RemI: { near: 4000, tol: 10 }, Half: { near: 500, tol: 5 }, Secs: { near: 1.0, tol: 0.011 }, Echo: 750, Echo2: 250, Echo3: 850, Echo4: 238, Echo5: 1500, Echo6: { near: 1000, tol: 10 }, Neg: { near: -1000, tol: 10 }, D4: 1500 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int { return runTest([]string{proj}) }); code != 0 || !strings.Contains(out, "1 passed") {
		t.Fatalf("naut test: exit %d\n%s\n%s", code, out, ld)
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
	for _, want := range []string{"SUB(T1.PRE,T1.ACC,Remain);", "SUB(T1.PRE,T1.ACC,RemI);", "MUL(T1.ACC,0.001,Secs);", "DIV(T1.ACC,2,Half);", "MOVE(Ms,T2.ACC);", "MOVE(250,T2.PRE);", "ADD(T2.ACC,100,T2.ACC);", "MOVE(16#EE,T2.PRE);", "MUL(Ms,2,T2.PRE);", "CPT(T2.PRE,T1.ACC);", "CPT(Neg,-T1.ACC);"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("written back without %q", want)
		}
	}
}
