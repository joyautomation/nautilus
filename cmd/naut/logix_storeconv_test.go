package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeConvL5X is a synthetic export: data boxes that convert as they
// store (a REAL into an INT rounds, halves to even; a DINT into an INT
// keeps the low 16 bits) and one that widens.
const storeConvL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<Tags>
<Tag Name="R1" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="2.5"/></Data>
</Tag>
<Tag Name="R2" TagType="Base" DataType="REAL" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="REAL" Radix="Float" Value="3.5"/></Data>
</Tag>
<Tag Name="D" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="70000"/></Data>
</Tag>
<Tag Name="S" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="-300"/></Data>
</Tag>
<Tag Name="I1" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="I2" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="I3" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="N" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="W" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Tm" TagType="Base" DataType="TIMER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="TIMER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="70000"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="Ct" TagType="Base" DataType="COUNTER" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Structure DataType="COUNTER"><DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="5"/><DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/><DataValueMember Name="DN" DataType="BOOL" Value="0"/></Structure></Data>
</Tag>
<Tag Name="Big" TagType="Base" DataType="DINT" Dimensions="2" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><Array DataType="DINT" Dimensions="2" Radix="Decimal"><Element Index="[0]" Value="0"/><Element Index="[1]" Value="70000"/></Array></Data>
</Tag>
<Tag Name="P" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="Q" TagType="Base" DataType="DINT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="DINT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="E" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="K" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="L" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
<Tag Name="C" TagType="Base" DataType="INT" Radix="Decimal" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated"><DataValue DataType="INT" Radix="Decimal" Value="0"/></Data>
</Tag>
</Tags>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[MOV(R1,I1);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[MOV(D,I2);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[MUL(R2,3,I3);]]></Text></Rung>
<Rung Number="3" Type="N"><Text><![CDATA[MOV(R2,N);]]></Text></Rung>
<Rung Number="4" Type="N"><Text><![CDATA[MOV(S,W);]]></Text></Rung>
<Rung Number="5" Type="N"><Text><![CDATA[MOV(Tm.PRE,P);]]></Text></Rung>
<Rung Number="6" Type="N"><Text><![CDATA[MOV(Tm.PRE,Q);]]></Text></Rung>
<Rung Number="8" Type="N"><Text><![CDATA[MOV(Ct.PRE,K);]]></Text></Rung>
<Rung Number="9" Type="N"><Text><![CDATA[MOV(70000,L);]]></Text></Rung>
<Rung Number="10" Type="N"><Text><![CDATA[CPT(C,R2*4.3);]]></Text></Rung>
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

// #777: a data box converts as it stores. The import writes the
// conversion nautilus needs to compute the same value (a bare REAL into an
// INT fails naut check; a bare DINT into an INT does not narrow), the
// project checks clean for the Logix target and runs to the controller's
// values, and the writer folds each conversion back into its box.
func TestLogixImportConvertsAsTheBoxStores(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(storeConvL5X), 0o644); err != nil {
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
		"{ I1 := REAL_TO_INT(R1) }",
		"{ I2 := DINT_TO_INT(D) }",
		"{ I3 := REAL_TO_INT(R2 * 3) }",
		"{ N := REAL_TO_DINT(R2) }",
		"{ W := S }", // widening: no conversion
		"{ P := TIME_TO_INT(Tm.PT) }",
		"{ Q := TIME_TO_DINT(Tm.PT) }",
		"{ K := DINT_TO_INT(Ct.PV) }",
		"{ L := DINT_TO_INT(70000) }",
		"{ C := REAL_TO_INT(R2 * 4.3) }",
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

	// The controller's values: 2.5 → 2 and 10.5 → 10 (halves to even),
	// 3.5 → 4, 70000 → 4464 (the low 16 bits, a literal too), -300
	// widened as is, and a CPT's REAL result rounded: 3.5 * 4.3 = 15.05.
	// (The timer and counter sources are checked for their form and
	// write-back only: nothing here runs those instances.)
	if err := os.WriteFile(filepath.Join(proj, "conv_test.yaml"), []byte(`tests:
  - name: each box stores what the controller stores
    steps:
      - scans: 1
        expect: { I1: 2, I2: 4464, I3: 10, N: 4, W: -300, L: 4464, C: 15 }
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
	for _, want := range []string{"MOVE(R1,I1);", "MOVE(D,I2);", "MUL(R2,3,I3);", "MOVE(R2,N);", "MOVE(S,W);", "MOVE(Tm.PRE,P);", "MOVE(Tm.PRE,Q);", "MOVE(Ct.PRE,K);", "MOVE(70000,L);", "MUL(R2,4.3,C);"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("written back without %q", want)
		}
	}
}
