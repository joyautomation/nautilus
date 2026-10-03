package writer

import (
	"fmt"
	"strconv"
	"strings"
)

// The L5X itself. Written by hand rather than through encoding/xml so the
// output has the exporter's own shape line for line: one element per
// line, CDATA on its own lines, attributes in Logix's order. That is what
// makes a generated project diff cleanly against an export of the same
// project — and it is what lang/l5x.Normalize was measured against.

func emit(lw *lowered, o Options) []byte {
	var b strings.Builder
	w := func(format string, a ...any) {
		fmt.Fprintf(&b, format, a...)
		b.WriteByte('\n')
	}
	w(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	w(`<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="%s" TargetName="%s" TargetType="Controller" ContainsContext="false" Owner="nautilus" ExportDate="%s" ExportOptions="References NoRawData L5KData DecoratedData Context Dependencies ForceProtectedEncoding AllProjDocTrans">`,
		attr(o.SoftwareRevision), attr(o.Controller), attr(o.ExportDate))
	w(`<Controller Use="Target" Name="%s" ProcessorType="%s" MajorRev="%s" MinorRev="%s" TimeSlice="20" ShareUnusedTimeSlice="1" ProjectCreationDate="(pinned)" LastModifiedDate="(pinned)" SFCExecutionControl="CurrentActive" SFCRestartPosition="MostRecent" SFCLastScan="DontScan" ProjectSN="16#0000_0000" MatchProjectToController="false" CanUseRPIFromProducer="false" InhibitAutomaticFirmwareUpdate="0" PassThroughConfiguration="EnabledWithAppend" DownloadProjectDocumentationAndExtendedProperties="true" DownloadProjectCustomProperties="true" ReportMinorOverflow="false" AutoDiagsEnabled="true" WebServerEnabled="false">`,
		attr(o.Controller), attr(o.ProcessorType), attr(o.MajorRev), attr(o.MinorRev))
	w(`<RedundancyInfo Enabled="false" KeepTestEditsOnSwitchOver="false"/>`)
	w(`<Security Code="0" ChangesToDetect="16#ffff_ffff_ffff_ffff"/>`)
	w(`<SafetyInfo/>`)
	w(`<DataTypes/>`)
	w(`<Modules>`)
	w(`<Module Name="Local" CatalogNumber="%s" Vendor="1" ProductType="14" ProductCode="168" Major="%s" Minor="%s" ParentModule="Local" ParentModPortId="1" Inhibited="false" MajorFault="true">`,
		attr(o.ProcessorType), attr(o.MajorRev), attr(o.MinorRev))
	w(`<EKey State="Disabled"/>`)
	w(`<Ports>`)
	w(`<Port Id="1" Address="0" Type="ICP" Upstream="false">`)
	w(`<Bus Size="17"/>`)
	w(`</Port>`)
	w(`<Port Id="2" Type="Ethernet" Upstream="false">`)
	w(`<Bus/>`)
	w(`</Port>`)
	w(`</Ports>`)
	w(`</Module>`)
	w(`</Modules>`)
	w(`<AddOnInstructionDefinitions/>`)
	emitTags(&b, lw.ctrlTags)
	w(`<Programs>`)
	w(`<Program Name="%s" TestEdits="false" MainRoutineName="%s" Disabled="false" UseAsFolder="false">`, attr(o.Program), attr(o.Routine))
	emitTags(&b, lw.progTags)
	w(`<Routines>`)
	w(`<Routine Name="%s" Type="RLL">`, attr(o.Routine))
	w(`<RLLContent>`)
	for i, r := range lw.rungs {
		w(`<Rung Number="%d" Type="N">`, i)
		if r.Comment != "" {
			w(`<Comment>`)
			w(`%s`, cdata(r.Comment))
			w(`</Comment>`)
		}
		w(`<Text>`)
		w(`%s`, cdata(r.Text+";"))
		w(`</Text>`)
		w(`</Rung>`)
	}
	w(`</RLLContent>`)
	w(`</Routine>`)
	w(`</Routines>`)
	w(`</Program>`)
	w(`</Programs>`)
	w(`<Tasks>`)
	if o.PeriodMs > 0 {
		w(`<Task Name="%s" Type="PERIODIC" Rate="%d" Priority="10" Watchdog="500" DisableUpdateOutputs="false" InhibitTask="false">`, attr(o.Task), o.PeriodMs)
	} else {
		w(`<Task Name="%s" Type="CONTINUOUS" Priority="10" Watchdog="500" DisableUpdateOutputs="false" InhibitTask="false">`, attr(o.Task))
	}
	w(`<ScheduledPrograms>`)
	w(`<ScheduledProgram Name="%s"/>`, attr(o.Program))
	w(`</ScheduledPrograms>`)
	w(`</Task>`)
	w(`</Tasks>`)
	w(`<CST MasterID="0"/>`)
	w(`<WallClockTime LocalTimeAdjustment="0" TimeZone="0"/>`)
	w(`<Trends/>`)
	w(`<TimeSynchronize Priority1="128" Priority2="128" PTPEnable="false"/>`)
	w(`<EthernetPorts>`)
	w(`<EthernetPort Port="1" Label="1" PortEnabled="true"/>`)
	w(`</EthernetPorts>`)
	w(`</Controller>`)
	w(`</RSLogix5000Content>`)
	return []byte(b.String())
}

func emitTags(b *strings.Builder, tags []tagDef) {
	if len(tags) == 0 {
		b.WriteString("<Tags/>\n")
		return
	}
	w := func(format string, a ...any) {
		fmt.Fprintf(b, format, a...)
		b.WriteByte('\n')
	}
	w(`<Tags>`)
	for _, t := range tags {
		radix := radixOf(t.DataType)
		dims := ""
		if t.Dim > 0 {
			dims = fmt.Sprintf(` Dimensions="%d"`, t.Dim)
		}
		if radix != "" {
			w(`<Tag Name="%s" TagType="Base" DataType="%s"%s Radix="%s" Constant="false" ExternalAccess="Read/Write">`, attr(t.Name), t.DataType, dims, radix)
		} else {
			w(`<Tag Name="%s" TagType="Base" DataType="%s"%s Constant="false" ExternalAccess="Read/Write">`, attr(t.Name), t.DataType, dims)
		}
		w(`<Data Format="L5K">`)
		w(`%s`, cdata(l5kValue(t)))
		w(`</Data>`)
		w(`<Data Format="Decorated">`)
		switch {
		case t.DataType == "TIMER":
			w(`<Structure DataType="TIMER">`)
			w(`<DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="%d"/>`, t.Preset)
			w(`<DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/>`)
			for _, m := range []string{"EN", "TT", "DN"} {
				w(`<DataValueMember Name="%s" DataType="BOOL" Value="0"/>`, m)
			}
			w(`</Structure>`)
		case t.DataType == "COUNTER":
			w(`<Structure DataType="COUNTER">`)
			w(`<DataValueMember Name="PRE" DataType="DINT" Radix="Decimal" Value="%d"/>`, t.Preset)
			w(`<DataValueMember Name="ACC" DataType="DINT" Radix="Decimal" Value="0"/>`)
			for _, m := range []string{"CU", "CD", "DN", "OV", "UN"} {
				w(`<DataValueMember Name="%s" DataType="BOOL" Value="0"/>`, m)
			}
			w(`</Structure>`)
		case t.Dim > 0:
			w(`<Array DataType="%s" Dimensions="%d" Radix="%s">`, t.DataType, t.Dim, radix)
			for i := 0; i < t.Dim; i++ {
				w(`<Element Index="[%d]" Value="%s"/>`, i, zeroOf(t.DataType))
			}
			w(`</Array>`)
		default:
			w(`<DataValue DataType="%s" Radix="%s" Value="%s"/>`, t.DataType, radix, decoratedValue(t))
		}
		w(`</Data>`)
		w(`</Tag>`)
	}
	w(`</Tags>`)
}

func radixOf(dt string) string {
	switch dt {
	case "REAL", "LREAL":
		return "Float"
	case "TIMER", "COUNTER":
		return ""
	default:
		return "Decimal"
	}
}

func zeroOf(dt string) string {
	switch dt {
	case "REAL", "LREAL":
		return "0.0"
	default:
		return "0"
	}
}

func decoratedValue(t tagDef) string {
	if t.Value == "" {
		return zeroOf(t.DataType)
	}
	return t.Value
}

// l5kValue is the L5K spelling of the tag's value: the exporter writes
// every value twice, and an importer may read either.
func l5kValue(t tagDef) string {
	switch t.DataType {
	case "TIMER", "COUNTER":
		return fmt.Sprintf("[0,%d,0]", t.Preset)
	}
	one := func() string {
		switch t.DataType {
		case "REAL", "LREAL":
			f := 0.0
			if t.Value != "" {
				f, _ = strconv.ParseFloat(t.Value, 64)
			}
			return l5kReal(f)
		default:
			if t.Value == "" {
				return "0"
			}
			return t.Value
		}
	}
	if t.Dim == 0 {
		return one()
	}
	elems := make([]string, t.Dim)
	for i := range elems {
		elems[i] = l5kZero(t.DataType)
	}
	return "[" + strings.Join(elems, ",") + "]"
}

func l5kZero(dt string) string {
	if dt == "REAL" || dt == "LREAL" {
		return l5kReal(0)
	}
	return "0"
}

// l5kReal is the exporter's float spelling: 8 decimals and a three-digit
// signed exponent (8.50000000e+001).
func l5kReal(f float64) string {
	s := strconv.FormatFloat(f, 'e', 8, 64)
	i := strings.LastIndexAny(s, "+-")
	exp := s[i+1:]
	for len(exp) < 3 {
		exp = "0" + exp
	}
	return s[:i+1] + exp
}

func attr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// cdata wraps text for a CDATA section; a literal "]]>" is split across
// two sections so it cannot end the block early.
func cdata(s string) string {
	return "<![CDATA[" + strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>") + "]]>"
}
