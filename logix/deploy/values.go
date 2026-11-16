package deploy

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// Keeping tag values across a download (#273, logix-authoring.md §6.4).
//
// A download replaces the whole project, tag data included, so a project
// written from nautilus would put every tag back to its initial value:
// setpoints, counts and recipe data the plant has changed are lost. The
// running controller's upload carries those values (its L5X export holds
// each tag's current data), so before the build the deploy copies them into
// the project it is about to download.
//
// The rule, tag by tag, for a tag in the new project:
//
//   - kept: the controller has a Base tag of the same name in the same
//     scope (controller, or the same program), of the same data type and
//     dimensions, and — for a structure (UDT) or Add-On Instruction type —
//     the type's definition is the same on both sides. Its data is copied
//     from the controller as the controller exported it.
//   - except a TIMER's or COUNTER's preset, which comes from the project: a
//     preset written as a literal (`PT := T#30S`) lives only in the tag's
//     PRE, so it is code, and a changed literal must take effect. The
//     accumulator and status bits are kept.
//   - reset (to the project's value, with the reason reported): the type,
//     the dimensions or the type's definition changed, the project marks
//     the tag constant, or the controller's export carries no data for it.
//   - new: only in the project; it starts at the project's value.
//
// Tags only on the controller are dropped by the download and reported.
// Alias tags carry no data and are left alone.

// ValuePlan says, tag by tag, what a download does with the controller's
// values. Labels are "Tag" for a controller-scope tag and "Program.Tag"
// for a program-scope one, as the logic diff names them.
type ValuePlan struct {
	// Kept are the tags whose controller values the download keeps.
	Kept []string
	// Reset are tags the controller has but whose value goes back to the
	// project's, each with the reason: "Spare: DINT → REAL".
	Reset []string
	// New are tags only in the project.
	New []string
	// Dropped are tags only on the controller: the download removes them.
	Dropped []string

	data map[string][]byte // key → the <Data> elements to put in the project
}

// Summary is the plan as one line.
func (p *ValuePlan) Summary() string {
	if p == nil {
		return "tag values: not kept"
	}
	s := fmt.Sprintf("tag values: %d kept from the controller", len(p.Kept))
	if len(p.Reset) > 0 {
		s += fmt.Sprintf(", %d reset to the project's value (%s)", len(p.Reset), strings.Join(p.Reset, "; "))
	}
	if len(p.New) > 0 {
		s += fmt.Sprintf(", %d new (%s)", len(p.New), strings.Join(p.New, ", "))
	}
	if len(p.Dropped) > 0 {
		s += fmt.Sprintf(", %d removed by the download (%s)", len(p.Dropped), strings.Join(p.Dropped, ", "))
	}
	return s
}

// PlanValues decides which of the running controller's tag values the
// project keeps. project is the generated project (parsed); running is the
// controller's upload as exported to L5X, raw, because its data is copied
// as the SDK wrote it.
func PlanValues(project *l5x.File, running []byte) (*ValuePlan, error) {
	have, err := l5x.Parse(running)
	if err != nil {
		return nil, fmt.Errorf("the controller's export: %w", err)
	}
	spans, err := tagSpans(running)
	if err != nil {
		return nil, fmt.Errorf("the controller's export: %w", err)
	}
	rawData := map[string][]dataElem{}
	for _, s := range spans {
		if len(s.data) > 0 {
			rawData[s.key] = s.data
		}
	}
	p := &ValuePlan{data: map[string][]byte{}}
	if project == nil || project.Controller == nil {
		return p, nil
	}
	ctl := map[string]*l5x.Tag{}
	if have.Controller != nil {
		for _, t := range scopedTags(have) {
			ctl[t.key] = t.tag
		}
	}
	seen := map[string]bool{}
	for _, pt := range scopedTags(project) {
		t := pt.tag
		if t.TagType == "Alias" {
			continue
		}
		seen[pt.key] = true
		label := tagName(t)
		c, ok := ctl[pt.key]
		if !ok || c.TagType == "Alias" {
			p.New = append(p.New, label)
			continue
		}
		reason := ""
		switch {
		case c.TagType != "Base" || (t.TagType != "" && t.TagType != "Base"):
			reason = fmt.Sprintf("%s tag on the controller", strings.ToLower(c.TagType))
		case !strings.EqualFold(c.DataType, t.DataType) || dims(c.Dimensions) != dims(t.Dimensions):
			reason = shape(c) + " → " + shape(t)
		case typeSig(have, c.DataType, nil) != typeSig(project, t.DataType, nil):
			reason = "the definition of " + t.DataType + " changed"
		case t.Constant:
			reason = "a constant takes the project's value"
		case strings.EqualFold(t.DataType, "MESSAGE"):
			// Its data is the MSG's parameters, which are the project's.
			reason = "a message takes the project's parameters"
		case rawData[pt.key] == nil:
			reason = "the controller's export carries no value for it"
		}
		if reason == "" {
			data, ok := keptData(t, rawData[pt.key])
			if !ok {
				reason = "its presets could not be matched"
			} else {
				p.data[pt.key] = data
				p.Kept = append(p.Kept, label)
				continue
			}
		}
		p.Reset = append(p.Reset, label+": "+reason)
	}
	for _, ht := range scopedTags(have) {
		if !seen[ht.key] && ht.tag.TagType != "Alias" {
			p.Dropped = append(p.Dropped, tagName(ht.tag))
		}
	}
	for _, l := range [][]string{p.Kept, p.Reset, p.New, p.Dropped} {
		sort.Strings(l)
	}
	return p, nil
}

// Apply writes the kept values into one L5X document of the project — the
// whole project, or the skeleton and program partial of a two-step build —
// and returns it. Tags the plan does not keep are left as they are.
func (p *ValuePlan) Apply(doc []byte) ([]byte, error) {
	if p == nil || len(p.data) == 0 {
		return doc, nil
	}
	spans, err := tagSpans(doc)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	at := 0
	for _, s := range spans {
		data, ok := p.data[s.key]
		if !ok {
			continue
		}
		switch {
		case len(s.data) > 0:
			out.Write(doc[at:s.data[0].start])
			out.Write(data)
			at = s.data[len(s.data)-1].end
		case s.selfClosing:
			// <Tag .../> — open it to take the data.
			out.Write(doc[at : s.close-2])
			out.WriteString(">\n")
			out.Write(data)
			out.WriteString("\n</Tag>")
			at = s.close
		default:
			out.Write(doc[at:s.close])
			out.Write(data)
			out.WriteString("\n")
			at = s.close
		}
	}
	out.Write(doc[at:])
	return out.Bytes(), nil
}

type scoped struct {
	key string
	tag *l5x.Tag
}

// scopedTags lists the controller- and program-scope tags of an export,
// keyed case-insensitively ("/name", "program/name"): Logix names are not
// case-sensitive.
func scopedTags(f *l5x.File) []scoped {
	var out []scoped
	if f == nil || f.Controller == nil {
		return out
	}
	for _, t := range f.Controller.Tags {
		out = append(out, scoped{tagKey("", t.Name), t})
	}
	for _, pr := range f.Controller.Programs {
		for _, t := range pr.Tags {
			out = append(out, scoped{tagKey(pr.Name, t.Name), t})
		}
	}
	return out
}

func tagKey(scope, name string) string { return strings.ToLower(scope) + "/" + strings.ToLower(name) }

func tagName(t *l5x.Tag) string {
	if t.Scope == "" {
		return t.Name
	}
	return t.Scope + "." + t.Name
}

func dims(d string) string { return strings.Join(strings.Fields(d), " ") }

func shape(t *l5x.Tag) string {
	if d := dims(t.Dimensions); d != "" {
		return t.DataType + "[" + strings.ReplaceAll(d, " ", ",") + "]"
	}
	return t.DataType
}

// typeSig is a type's layout as a comparable string: a built-in type is its
// name; a UDT its visible members (name, type, dimension), nested types
// expanded; an Add-On Instruction its parameters and local tags. Two sides
// with equal signatures lay a tag's data out the same way.
func typeSig(f *l5x.File, name string, seen map[string]bool) string {
	lname := strings.ToLower(name)
	if f == nil || f.Controller == nil {
		return lname
	}
	if seen[lname] {
		return lname + "{…}"
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	seen[lname] = true
	defer delete(seen, lname)
	for _, d := range f.Controller.DataTypes {
		if !strings.EqualFold(d.Name, name) {
			continue
		}
		var parts []string
		for _, m := range d.Members {
			if m.Hidden {
				continue
			}
			typ := m.DataType
			if strings.EqualFold(typ, "BIT") {
				typ = "BOOL"
			}
			parts = append(parts, fmt.Sprintf("%s:%s[%d]", strings.ToLower(m.Name), typeSig(f, typ, seen), m.Dimension))
		}
		return "udt{" + strings.Join(parts, ",") + "}"
	}
	for _, a := range f.Controller.AOIs {
		if !strings.EqualFold(a.Name, name) {
			continue
		}
		var parts []string
		for _, prm := range a.Parameters {
			parts = append(parts, fmt.Sprintf("%s:%s[%d]:%s", strings.ToLower(prm.Name), typeSig(f, prm.DataType, seen), prm.Dimension, strings.ToLower(prm.Usage)))
		}
		for _, t := range a.LocalTags {
			parts = append(parts, fmt.Sprintf("local %s:%s[%s]", strings.ToLower(t.Name), typeSig(f, t.DataType, seen), dims(t.Dimensions)))
		}
		return "aoi{" + strings.Join(parts, ",") + "}"
	}
	return lname
}

var (
	preDecorated = regexp.MustCompile(`(<DataValueMember Name="PRE"[^>]*?Value=")(-?\d+)(")`)
	preL5K       = regexp.MustCompile(`\[(-?\d+),(-?\d+),(-?\d+)\]`)
)

// keptData is the controller's <Data> elements for a kept tag, as they
// go into the project: verbatim, except a TIMER's or COUNTER's preset,
// which is the project's. ok is false when the presets cannot be matched
// element by element.
func keptData(t *l5x.Tag, data []dataElem) ([]byte, bool) {
	typ := strings.ToUpper(t.DataType)
	if typ != "TIMER" && typ != "COUNTER" {
		return joinData(data), true
	}
	pres, ok := presets(t.Value)
	if !ok {
		// The project carries no preset (no data): nothing to override.
		return joinData(data), true
	}
	var out []dataElem
	for _, d := range data {
		raw := d.raw
		switch d.format {
		case "Decorated":
			if len(preDecorated.FindAllIndex(raw, -1)) != len(pres) {
				return nil, false
			}
			i := 0
			raw = preDecorated.ReplaceAllFunc(raw, func(m []byte) []byte {
				sub := preDecorated.FindSubmatch(m)
				r := append(append(append([]byte{}, sub[1]...), strconv.FormatInt(pres[i], 10)...), sub[3]...)
				i++
				return r
			})
		case "L5K":
			if len(preL5K.FindAllIndex(raw, -1)) != len(pres) {
				// Not the [control,PRE,ACC] spelling expected: leave the
				// L5K copy out and let the Decorated one carry the value.
				continue
			}
			i := 0
			raw = preL5K.ReplaceAllFunc(raw, func(m []byte) []byte {
				sub := preL5K.FindSubmatch(m)
				r := fmt.Sprintf("[%s,%d,%s]", sub[1], pres[i], sub[3])
				i++
				return []byte(r)
			})
		}
		out = append(out, dataElem{format: d.format, raw: raw})
	}
	for _, d := range out {
		if d.format == "Decorated" {
			return joinData(out), true
		}
	}
	return nil, false
}

// presets reads the project's PRE values from a TIMER or COUNTER tag's
// decoded data, element by element for an array.
func presets(v any) ([]int64, bool) {
	one := func(v any) (int64, bool) {
		m, ok := v.(map[string]any)
		if !ok {
			return 0, false
		}
		for k, x := range m {
			if strings.EqualFold(k, "PRE") {
				switch n := x.(type) {
				case int64:
					return n, true
				case float64:
					return int64(n), true
				}
			}
		}
		return 0, false
	}
	switch x := v.(type) {
	case map[string]any:
		n, ok := one(x)
		return []int64{n}, ok
	case []any:
		out := make([]int64, 0, len(x))
		for _, e := range x {
			n, ok := one(e)
			if !ok {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	return nil, false
}

func joinData(data []dataElem) []byte {
	var parts [][]byte
	for _, d := range data {
		parts = append(parts, d.raw)
	}
	return bytes.Join(parts, []byte("\n"))
}

// dataElem is one <Data> element of a tag, raw.
type dataElem struct {
	format     string
	start, end int
	raw        []byte
}

// tagSpan is where one controller- or program-scope <Tag> sits in a
// document: its <Data> elements, and the start of its </Tag>.
type tagSpan struct {
	key         string
	data        []dataElem
	close       int // the offset of </Tag>, or just past "/>" when self-closing
	selfClosing bool
}

// tagSpans finds every controller- and program-scope <Tag> in an L5X
// document (a whole project or a partial export) by byte offset, so data
// can be copied out of one document and spliced into another without
// re-serialising either.
func tagSpans(doc []byte) ([]tagSpan, error) {
	bom := 0
	if bytes.HasPrefix(doc, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	dec := xml.NewDecoder(bytes.NewReader(doc[bom:]))
	type frame struct{ name, program string }
	var stack []frame
	var out []tagSpan
	var cur *tagSpan
	dataStart := -1
	depth := 0 // within the current Tag
	for {
		off := int(dec.InputOffset()) + bom
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch se := tok.(type) {
		case xml.StartElement:
			fr := frame{name: se.Name.Local}
			if len(stack) > 0 {
				fr.program = stack[len(stack)-1].program
			}
			if fr.name == "Program" {
				fr.program = attrOf(se, "Name")
			}
			if cur != nil {
				depth++
				if depth == 1 && fr.name == "Data" {
					dataStart = off
					cur.data = append(cur.data, dataElem{format: attrOf(se, "Format"), start: off})
				}
			} else if fr.name == "Tag" && len(stack) >= 2 && stack[len(stack)-1].name == "Tags" {
				parent := stack[len(stack)-2].name
				if parent == "Controller" || parent == "Program" {
					scope := ""
					if parent == "Program" {
						scope = fr.program
					}
					cur = &tagSpan{key: tagKey(scope, attrOf(se, "Name"))}
					depth = 0
				}
			}
			stack = append(stack, fr)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if cur == nil {
				continue
			}
			end := int(dec.InputOffset()) + bom
			if depth == 0 {
				cur.close = off
				if end == off {
					// A self-closing <Tag/>: no end tag of its own.
					cur.selfClosing = true
				}
				out = append(out, *cur)
				cur = nil
				continue
			}
			if depth == 1 && se.Name.Local == "Data" && dataStart >= 0 {
				d := &cur.data[len(cur.data)-1]
				d.end = end
				d.raw = doc[d.start:d.end]
				dataStart = -1
			}
			depth--
		}
	}
	return out, nil
}

func attrOf(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
