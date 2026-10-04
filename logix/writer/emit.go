package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
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
	w(`<Controller Use="Target" Name="%s" ProcessorType="%s" MajorRev="%s" MinorRev="%s" ProjectCreationDate="(pinned)" LastModifiedDate="(pinned)" SFCExecutionControl="CurrentActive" SFCRestartPosition="MostRecent" SFCLastScan="DontScan" ProjectSN="16#0000_0000" MatchProjectToController="false" CanUseRPIFromProducer="false" InhibitAutomaticFirmwareUpdate="0" PassThroughConfiguration="EnabledWithAppend" DownloadProjectDocumentationAndExtendedProperties="true" DownloadProjectCustomProperties="true" ReportMinorOverflow="false" AutoDiagsEnabled="true" WebServerEnabled="false">`,
		attr(o.Controller), attr(o.ProcessorType), attr(o.MajorRev), attr(o.MinorRev))
	w(`<RedundancyInfo Enabled="false" KeepTestEditsOnSwitchOver="false"/>`)
	w(`<Security Code="0" ChangesToDetect="16#ffff_ffff_ffff_ffff"/>`)
	w(`<SafetyInfo/>`)
	if types := lw.usedTypesInOrder(); len(types) > 0 {
		w(`<DataTypes>`)
		for _, u := range types {
			w(`<DataType Name="%s" Family="NoFamily" Class="User">`, attr(u.Name))
			w(`<Members>`)
			for _, m := range u.Members {
				switch {
				case m.Struct != nil:
					w(`<Member Name="%s" DataType="%s" Dimension="%d" Radix="NullType" Hidden="false" ExternalAccess="Read/Write"/>`, attr(m.Name), m.DataType, m.Dim)
				default:
					w(`<Member Name="%s" DataType="%s" Dimension="%d" Radix="%s" Hidden="false" ExternalAccess="Read/Write"/>`, attr(m.Name), m.DataType, m.Dim, radixOf(m.DataType))
				}
			}
			w(`</Members>`)
			w(`</DataType>`)
		}
		w(`</DataTypes>`)
	} else {
		w(`<DataTypes/>`)
	}
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
	if lw.st {
		w(`<Routine Name="%s" Type="ST">`, attr(o.Routine))
		w(`<STContent>`)
		for i, line := range lw.stLines {
			w(`<Line Number="%d">`, i)
			w(`%s`, cdata(line))
			w(`</Line>`)
		}
		w(`</STContent>`)
		w(`</Routine>`)
	} else {
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
	}
	w(`</Routines>`)
	w(`</Program>`)
	if len(lw.sideRungs) > 0 {
		w(`<Program Name="%s" TestEdits="false" MainRoutineName="MainRoutine" Disabled="false" UseAsFolder="false">`, SideProgram)
		w(`<Description>`)
		w(`%s`, cdata("nautilus side code: testing, verification and metrics. Generated; not part of the plant logic."))
		w(`</Description>`)
		w(`<Tags/>`)
		w(`<Routines>`)
		w(`<Routine Name="MainRoutine" Type="RLL">`)
		w(`<RLLContent>`)
		for i, r := range lw.sideRungs {
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
	}
	w(`</Programs>`)
	w(`<Tasks>`)
	if o.PeriodMs > 0 {
		w(`<Task Name="%s" Type="PERIODIC" Rate="%d" Priority="10" Watchdog="500" DisableUpdateOutputs="false" InhibitTask="false">`, attr(o.Task), o.PeriodMs)
	} else {
		w(`<Task Name="%s" Type="CONTINUOUS" Priority="10" Watchdog="500" DisableUpdateOutputs="false" InhibitTask="false">`, attr(o.Task))
	}
	w(`<ScheduledPrograms>`)
	w(`<ScheduledProgram Name="%s"/>`, attr(o.Program))
	if len(lw.sideRungs) > 0 {
		w(`<ScheduledProgram Name="%s"/>`, SideProgram)
	}
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
		if t.Struct != nil {
			w(`<Tag Name="%s" TagType="Base" DataType="%s"%s Constant="false" ExternalAccess="Read/Write">`, attr(t.Name), t.DataType, dims)
			if t.Desc != "" {
				w(`<Description>`)
				w(`%s`, cdata(t.Desc))
				w(`</Description>`)
			}
			// Decorated only: the L5K spelling of a structure packs its
			// BOOLs into hidden host bytes in layout order, and the
			// Decorated form is the one the importer reads.
			w(`<Data Format="Decorated">`)
			if t.Dim > 0 {
				w(`<Array DataType="%s" Dimensions="%d">`, t.DataType, t.Dim)
				for i := 0; i < t.Dim; i++ {
					w(`<Element Index="[%d]">`, i)
					w(`<Structure DataType="%s">`, t.DataType)
					structValue(t.Struct, nil, w)
					w(`</Structure>`)
					w(`</Element>`)
				}
				w(`</Array>`)
			} else {
				w(`<Structure DataType="%s">`, t.DataType)
				structValue(t.Struct, t.Init, w)
				w(`</Structure>`)
			}
			w(`</Data>`)
			w(`</Tag>`)
			continue
		}
		if radix != "" {
			w(`<Tag Name="%s" TagType="Base" DataType="%s"%s Radix="%s" Constant="false" ExternalAccess="Read/Write">`, attr(t.Name), t.DataType, dims, radix)
		} else {
			w(`<Tag Name="%s" TagType="Base" DataType="%s"%s Constant="false" ExternalAccess="Read/Write">`, attr(t.Name), t.DataType, dims)
		}
		if t.Desc != "" {
			w(`<Description>`)
			w(`%s`, cdata(t.Desc))
			w(`</Description>`)
		}
		if strings.HasPrefix(t.DataType, "FBD_") {
			w(`</Tag>`)
			continue
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
	case "TIMER", "COUNTER", "FBD_TIMER", "FBD_COUNTER":
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

// WriteRungs lowers ladder source to a Rung-target partial export: the
// shape the SDK's import-rungs takes, and what an online edit sends to a
// running controller. Every rung is Use="Target"; the tags the rungs name
// ride along as Use="Context", exactly as Logix exports them, so the
// importer can resolve operands without creating anything. New tags are
// not an online edit's to create — a program whose tag set changed needs
// a download, which deploy decides by comparing tag sets.
func WriteRungs(src string, opts Options) ([]byte, []Diag, error) {
	m, err := ld.Graph(src, opts.Libs...)
	if err != nil {
		return nil, nil, err
	}
	if m.Name == "" {
		return nil, nil, fmt.Errorf("logix writer: source declares no PROGRAM")
	}
	opts = opts.withDefaults(m.Name)
	lw := lower(m, opts)
	if len(lw.diags) > 0 {
		return nil, lw.diags, nil
	}
	return emitRungs(lw, opts), nil, nil
}

func emitRungs(lw *lowered, o Options) []byte {
	var b strings.Builder
	w := func(format string, a ...any) {
		fmt.Fprintf(&b, format, a...)
		b.WriteByte('\n')
	}
	w(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	w(`<RSLogix5000Content SchemaRevision="1.0" SoftwareRevision="%s" TargetType="Rung" TargetCount="%d" ContainsContext="true" ExportDate="%s" ExportOptions="References NoRawData L5KData DecoratedData Context ProductDefinedTypes RoutineLabels AliasExtras IOTags NoStringData ForceProtectedEncoding AllProjDocTrans">`,
		attr(o.SoftwareRevision), len(lw.rungs), attr(o.ExportDate))
	w(`<Controller Use="Context" Name="%s">`, attr(o.Controller))
	w(`<DataTypes Use="Context">`)
	for _, dt := range usedTypes(lw) {
		w(`<DataType Name="%s" Family="NoFamily" Class="ProductDefined"/>`, dt)
	}
	for _, u := range lw.usedTypesInOrder() {
		w(`<DataType Name="%s" Family="NoFamily" Class="User"/>`, attr(u.Name))
	}
	w(`</DataTypes>`)
	if len(lw.ctrlTags) > 0 {
		emitContextTags(&b, lw.ctrlTags)
	}
	w(`<Programs Use="Context">`)
	w(`<Program Use="Context" Name="%s">`, attr(o.Program))
	if len(lw.progTags) > 0 {
		emitContextTags(&b, lw.progTags)
	}
	w(`<Routines Use="Context">`)
	w(`<Routine Use="Context" Name="%s">`, attr(o.Routine))
	w(`<RLLContent Use="Context">`)
	for i, r := range lw.rungs {
		w(`<Rung Use="Target" Number="%d" Type="N">`, i)
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
	w(`</Controller>`)
	w(`</RSLogix5000Content>`)
	return []byte(b.String())
}

// emitContextTags writes a tag list marked Use="Context": the exporter's
// form for tags a partial export references but does not carry.
func emitContextTags(b *strings.Builder, tags []tagDef) {
	var inner strings.Builder
	emitTags(&inner, tags)
	s := strings.Replace(inner.String(), "<Tags>", `<Tags Use="Context">`, 1)
	b.WriteString(s)
}

// usedTypes lists the atomic types the tags use, sorted, for the partial
// export's DataTypes context.
func usedTypes(lw *lowered) []string {
	seen := map[string]bool{}
	for _, t := range append(append([]tagDef{}, lw.ctrlTags...), lw.progTags...) {
		if t.Struct == nil {
			seen[t.DataType] = true
		}
	}
	return sortedKeys(seen)
}
