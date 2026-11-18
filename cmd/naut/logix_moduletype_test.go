package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// devMapL5X is a synthetic export: an Add-On Instruction whose InOut
// parameter has a module-defined type (a device's I/O structure, kept
// with the module, never in DataTypes), with rungs that name it, and one
// that does not.
const devMapL5X = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="38.01" TargetName="Mini" TargetType="Controller" ContainsContext="false" ExportDate="(pinned)">
<Controller Name="Mini" ProcessorType="1756-L85E" MajorRev="38" MinorRev="11">
<DataTypes/>
<AddOnInstructionDefinitions>
<AddOnInstructionDefinition Name="DevMap" Revision="1.0" ExecutePrescan="false" ExecutePostscan="false" ExecuteEnableInFalse="false">
<Parameters>
<Parameter Name="EnableIn" TagType="Base" DataType="BOOL" Usage="Input" Radix="Decimal" Required="false" Visible="false" ExternalAccess="Read Only"/>
<Parameter Name="EnableOut" TagType="Base" DataType="BOOL" Usage="Output" Radix="Decimal" Required="false" Visible="false" ExternalAccess="Read Only"/>
<Parameter Name="modIn" TagType="Base" DataType="_0001:DeviceX_1234ABCD:I:0" Usage="InOut" Required="true" Visible="true" Constant="false"/>
<Parameter Name="stsRun" TagType="Base" DataType="BOOL" Usage="Output" Radix="Decimal" Required="false" Visible="true" ExternalAccess="Read Only"/>
<Parameter Name="stsCode" TagType="Base" DataType="SINT" Usage="Output" Radix="Decimal" Required="false" Visible="true" ExternalAccess="Read Only"/>
<Parameter Name="cmdGo" TagType="Base" DataType="BOOL" Usage="Input" Radix="Decimal" Required="false" Visible="true" ExternalAccess="Read/Write"/>
<Parameter Name="stsEcho" TagType="Base" DataType="BOOL" Usage="Output" Radix="Decimal" Required="false" Visible="true" ExternalAccess="Read Only"/>
</Parameters>
<LocalTags/>
<Routines>
<Routine Name="Logic" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[XIC(modIn.Data[2].2)OTE(stsRun);]]></Text></Rung>
<Rung Number="1" Type="N"><Text><![CDATA[CPS(modIn.Data[0],stsCode,1);]]></Text></Rung>
<Rung Number="2" Type="N"><Text><![CDATA[XIC(cmdGo)OTE(stsEcho);]]></Text></Rung>
</RLLContent>
</Routine>
</Routines>
</AddOnInstructionDefinition>
</AddOnInstructionDefinitions>
<Tags/>
<Programs>
<Program Name="Main" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">
<Tags/>
<Routines>
<Routine Name="MainRoutine" Type="RLL">
<RLLContent>
<Rung Number="0" Type="N"><Text><![CDATA[NOP();]]></Text></Rung>
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

// #776: an AOI parameter whose type is module-defined has no nautilus
// declaration, so the FUNCTION_BLOCK leaves it out. Every rung naming it
// is refused with that reason (a contact on it used to be carried, and
// naut check / naut run failed on "undeclared identifier"), and the rest
// of the block imports, and the project checks clean.
func TestLogixImportRefusesRungsOnAModuleDefinedParameter(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Mini.L5X")
	if err := os.WriteFile(src, []byte(devMapL5X), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "mini")
	out, code := capture(t, func() int { return runLogix([]string{"import", "--project", proj, src}) })
	if code != 0 {
		t.Fatalf("import: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "modIn: parameter type _0001:DeviceX_1234ABCD:I:0 is module-defined (a device's I/O structure), which the import does not carry, so it is left out, and so is every rung that names it") {
		t.Errorf("the import does not explain the parameter:\n%s", out)
	}
	body, err := os.ReadFile(filepath.Join(proj, "lib", "DevMap.ld"))
	if err != nil {
		t.Fatal(err)
	}
	ld := string(body)
	const why = "modIn: type _0001:DeviceX_1234ABCD:I:0 is module-defined (a device's I/O structure), which the import does not carry"
	for _, want := range []string{
		"// not imported (type: " + why + "): XIC(modIn.Data[2].2)OTE(stsRun);",
		"// not imported (type: modIn.Data[0]: type _0001:DeviceX_1234ABCD:I:0 is module-defined",
		"cmdGo ( stsEcho )",
	} {
		if !strings.Contains(ld, want) {
			t.Errorf("lib/DevMap.ld lacks %q:\n%s", want, ld)
		}
	}
	if out, code := capture(t, func() int { return runCheck([]string{proj}) }); code != 0 {
		t.Errorf("naut check on the import: exit %d\n%s", code, out)
	}
}
