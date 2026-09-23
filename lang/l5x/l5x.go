// Package l5x reads Rockwell L5X exports — the XML form of a Logix project
// — in pure Go. No Studio 5000, no licensing, no Windows.
//
// An L5X is what Logix Designer writes on File → Export, and what the
// Studio 5000 SDK writes from `partial_export_to_xml_file`. It carries the
// whole offline project: UDTs, tags (with the descriptions a live CIP
// browse cannot recover), programs, and routine bodies as text. Reading it
// lights up nautilus's existing tooling on Allen-Bradley code:
//
//	Parse      → the document model, faithful to the export
//	Types      → lang/stgen structs → IEC ST type declarations
//	TagsYAML   → a nautilus tag file, descriptions included
//	Ladder     → the lang/ld render model, so the ladder viewer and the
//	             revision diff work on Logix rungs
//	Normalize  → the volatile attributes pinned, so two exports of an
//	             unchanged project compare equal
//
// Nothing here executes Logix semantics. The reader renders; it does not
// claim equivalence. That is the whole reason it carries no risk.
package l5x

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// File is a parsed L5X export: the <RSLogix5000Content> envelope and the
// controller it wraps.
//
// TargetType distinguishes a whole-project export ("Controller") from a
// partial one ("Program", "Routine", "DataType", …) — the shape a partial
// import takes. Both parse the same way: a partial export still nests its
// target inside a Controller marked Use="Context".
type File struct {
	SchemaRevision   string
	SoftwareRevision string
	TargetName       string
	TargetType       string
	ExportDate       string
	ExportOptions    string
	ContainsContext  bool

	Controller *Controller
}

// Partial reports whether this is a partial export — a fragment exported
// for re-import, not the whole project.
func (f *File) Partial() bool {
	return f.TargetType != "" && f.TargetType != "Controller"
}

// Detailed reports whether this is a DETAILED export — the one Logix writes
// with Context and ProductDefinedTypes, carrying every module- and
// product-defined type the project references. A detailed export of a small
// project runs to megabytes; the basic export of the same project is a few
// kilobytes, because it omits those types entirely.
//
// The two are not comparable. Anything that exports a project in order to
// compare it against one on disk — drift detection, most obviously — has to
// ask for the same kind the file on disk already is, or it will report a
// difference of 1.8MB where there is no difference at all.
func (f *File) Detailed() bool {
	return strings.Contains(f.ExportOptions, "ProductDefinedTypes")
}

// Controller is the <Controller> element: the project's contents.
type Controller struct {
	Name          string
	ProcessorType string
	MajorRev      string
	MinorRev      string
	Description   string

	DataTypes []*DataType
	AOIs      []*AddOnInstruction
	Tags      []*Tag // controller-scoped
	Programs  []*Program
}

// DataType is one <DataType>: a UDT, or one of the module-defined and
// product-defined shapes an export carries alongside them (Class tells
// them apart — "User" is the hand-authored kind).
type DataType struct {
	Name        string
	Family      string
	Class       string
	Description string
	Members     []Member
}

// User reports whether this is a hand-authored UDT rather than a
// module-defined or product-defined shape.
func (d *DataType) User() bool { return d.Class == "User" || d.Class == "" }

// Member is one UDT member.
//
// Logix has no BOOL member in a UDT's layout: an authored BOOL becomes a
// Hidden host (SINT/INT/DINT) plus a visible Member of DataType "BIT" that
// Targets it at a BitNumber. Rendering the authored type back means
// dropping the hosts and treating the BIT members as BOOL — see Types.
type Member struct {
	Name        string
	DataType    string
	Dimension   int // 0 = scalar, n = ARRAY [0..n-1]
	Radix       string
	Hidden      bool
	Target      string // the host member a BIT overlays
	BitNumber   int
	Description string
}

// AddOnInstruction is one <AddOnInstructionDefinition>. The reader carries
// its identity, parameters and routines so an AOI call in a rung can be
// explained; it does not attempt to inline the AOI's logic.
type AddOnInstruction struct {
	Name        string
	Revision    string
	Description string
	Parameters  []Parameter
	LocalTags   []*Tag
	Routines    []*Routine
}

// Parameter is one AOI parameter.
type Parameter struct {
	Name        string
	DataType    string
	Dimension   int
	Usage       string // "Input" | "Output" | "InOut"
	Required    bool
	Visible     bool
	Description string
}

// Tag is one <Tag>, controller- or program-scoped.
//
// Description is the payload a live browse cannot reach: Logix keeps tag
// documentation in the offline project, so `naut eip import` has to
// leave desc: empty (eip/codegen/tags.go). Reading the L5X recovers it.
type Tag struct {
	Name           string
	TagType        string // "Base" | "Alias" | "Produced" | "Consumed"
	DataType       string
	Dimensions     string // "10", or "2 3" for a multi-dimensional tag
	Radix          string
	AliasFor       string
	Constant       bool
	ExternalAccess string
	Description    string
	// Comments are per-operand descriptions: a comment on a member or bit
	// of this tag, keyed by the operand suffix (".DN", "[3].MEMBER").
	Comments map[string]string
	// Value is the decoded <Data Format="Decorated"> initial value:
	// bool/int64/float64/string for a scalar, map[string]any for a
	// structure, []any for an array. nil when the export carries none.
	Value any
	// Scope is "" for a controller-scoped tag, else the owning program or
	// AOI name.
	Scope string
}

// Program is one <Program> and its routines.
type Program struct {
	Name            string
	MainRoutineName string
	Disabled        bool
	Description     string
	Tags            []*Tag
	Routines        []*Routine
}

// Routine is one <Routine>. Rungs is populated for Type "RLL"; Text holds
// the verbatim body of an "ST" routine. FBD and SFC bodies are kept as raw
// XML in Raw so nothing is lost, but are not modelled yet.
type Routine struct {
	Name        string
	Type        string // "RLL" | "ST" | "FBD" | "SFC"
	Description string
	Rungs       []Rung
	Text        string
	Raw         []byte
	// Owner is the program or AOI the routine belongs to.
	Owner string
	Line  int // 1-based line of <Routine> in the source
}

// Rung is one ladder rung: the neutral-text body Logix exports, its
// comment, and where it sits in the file.
type Rung struct {
	Number  int
	Type    string // "N" normal, "I"/"D"/"R"/"e" for edit zones
	Comment string
	Text    string
	Line    int // 1-based line of <Rung> in the source
}

// ParseFile reads and parses the L5X at path.
func ParseFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Parse parses an L5X document.
//
// It walks the document rather than unmarshalling it wholesale, so every
// routine and rung carries the line it came from: the ladder viewer's
// click-to-source and the revision diff both address rungs by line, and an
// L5X is as legitimate a source file as a .ld.
func Parse(src []byte) (*File, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	lines := newLineIndex(src)
	dec := xml.NewDecoder(bytes.NewReader(src))

	f := &File{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "RSLogix5000Content" {
			return nil, fmt.Errorf("not an L5X: root element is <%s>", se.Name.Local)
		}
		f.SchemaRevision = attr(se, "SchemaRevision")
		f.SoftwareRevision = attr(se, "SoftwareRevision")
		f.TargetName = attr(se, "TargetName")
		f.TargetType = attr(se, "TargetType")
		f.ExportDate = attr(se, "ExportDate")
		f.ExportOptions = attr(se, "ExportOptions")
		f.ContainsContext = attr(se, "ContainsContext") == "true"
		ctrl, err := parseController(dec, lines)
		if err != nil {
			return nil, err
		}
		f.Controller = ctrl
		break
	}
	if f.Controller == nil {
		return nil, fmt.Errorf("not an L5X: no <RSLogix5000Content> root")
	}
	return f, nil
}

// parseController consumes the children of <RSLogix5000Content> up to its
// end, filling the controller it finds.
func parseController(dec *xml.Decoder, lines *lineIndex) (*Controller, error) {
	c := &Controller{}
	seen := false
	err := walk(dec, func(se xml.StartElement) error {
		if se.Name.Local != "Controller" {
			return skip(dec)
		}
		seen = true
		c.Name = attr(se, "Name")
		c.ProcessorType = attr(se, "ProcessorType")
		c.MajorRev = attr(se, "MajorRev")
		c.MinorRev = attr(se, "MinorRev")
		return walk(dec, func(se xml.StartElement) error {
			switch se.Name.Local {
			case "Description":
				txt, err := text(dec, se)
				c.Description = txt
				return err
			case "DataTypes":
				return walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "DataType" {
						return skip(dec)
					}
					dt, err := parseDataType(dec, se)
					if err != nil {
						return err
					}
					c.DataTypes = append(c.DataTypes, dt)
					return nil
				})
			case "AddOnInstructionDefinitions":
				return walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "AddOnInstructionDefinition" {
						return skip(dec)
					}
					aoi, err := parseAOI(dec, se, lines)
					if err != nil {
						return err
					}
					c.AOIs = append(c.AOIs, aoi)
					return nil
				})
			case "Tags":
				tags, err := parseTags(dec, "")
				c.Tags = append(c.Tags, tags...)
				return err
			case "Programs":
				return walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Program" {
						return skip(dec)
					}
					p, err := parseProgram(dec, se, lines)
					if err != nil {
						return err
					}
					c.Programs = append(c.Programs, p)
					return nil
				})
			default:
				return skip(dec)
			}
		})
	})
	if err != nil {
		return nil, err
	}
	if !seen {
		return nil, fmt.Errorf("L5X has no <Controller> element")
	}
	return c, nil
}

func parseDataType(dec *xml.Decoder, se xml.StartElement) (*DataType, error) {
	dt := &DataType{
		Name:   attr(se, "Name"),
		Family: attr(se, "Family"),
		Class:  attr(se, "Class"),
	}
	err := walk(dec, func(se xml.StartElement) error {
		switch se.Name.Local {
		case "Description":
			txt, err := text(dec, se)
			dt.Description = txt
			return err
		case "Members":
			return walk(dec, func(se xml.StartElement) error {
				if se.Name.Local != "Member" {
					return skip(dec)
				}
				m := Member{
					Name:      attr(se, "Name"),
					DataType:  attr(se, "DataType"),
					Dimension: atoi(attr(se, "Dimension")),
					Radix:     attr(se, "Radix"),
					Hidden:    attr(se, "Hidden") == "true",
					Target:    attr(se, "Target"),
					BitNumber: atoi(attr(se, "BitNumber")),
				}
				err := walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Description" {
						return skip(dec)
					}
					txt, err := text(dec, se)
					m.Description = txt
					return err
				})
				dt.Members = append(dt.Members, m)
				return err
			})
		default:
			return skip(dec)
		}
	})
	return dt, err
}

func parseAOI(dec *xml.Decoder, se xml.StartElement, lines *lineIndex) (*AddOnInstruction, error) {
	aoi := &AddOnInstruction{
		Name:     attr(se, "Name"),
		Revision: attr(se, "Revision"),
	}
	err := walk(dec, func(se xml.StartElement) error {
		switch se.Name.Local {
		case "Description":
			txt, err := text(dec, se)
			aoi.Description = txt
			return err
		case "Parameters":
			return walk(dec, func(se xml.StartElement) error {
				if se.Name.Local != "Parameter" {
					return skip(dec)
				}
				p := Parameter{
					Name:      attr(se, "Name"),
					DataType:  attr(se, "DataType"),
					Dimension: atoi(attr(se, "Dimension")),
					Usage:     attr(se, "Usage"),
					Required:  attr(se, "Required") == "true",
					Visible:   attr(se, "Visible") == "true",
				}
				err := walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Description" {
						return skip(dec)
					}
					txt, err := text(dec, se)
					p.Description = txt
					return err
				})
				aoi.Parameters = append(aoi.Parameters, p)
				return err
			})
		case "LocalTags":
			tags, err := parseTags(dec, aoi.Name)
			aoi.LocalTags = append(aoi.LocalTags, tags...)
			return err
		case "Routines":
			rs, err := parseRoutines(dec, aoi.Name, lines)
			aoi.Routines = append(aoi.Routines, rs...)
			return err
		default:
			return skip(dec)
		}
	})
	return aoi, err
}

func parseProgram(dec *xml.Decoder, se xml.StartElement, lines *lineIndex) (*Program, error) {
	p := &Program{
		Name:            attr(se, "Name"),
		MainRoutineName: attr(se, "MainRoutineName"),
		Disabled:        attr(se, "Disabled") == "true",
	}
	err := walk(dec, func(se xml.StartElement) error {
		switch se.Name.Local {
		case "Description":
			txt, err := text(dec, se)
			p.Description = txt
			return err
		case "Tags":
			tags, err := parseTags(dec, p.Name)
			p.Tags = append(p.Tags, tags...)
			return err
		case "Routines":
			rs, err := parseRoutines(dec, p.Name, lines)
			p.Routines = append(p.Routines, rs...)
			return err
		default:
			return skip(dec)
		}
	})
	return p, err
}

func parseTags(dec *xml.Decoder, scope string) ([]*Tag, error) {
	var out []*Tag
	err := walk(dec, func(se xml.StartElement) error {
		if se.Name.Local != "Tag" && se.Name.Local != "LocalTag" {
			return skip(dec)
		}
		t := &Tag{
			Name:           attr(se, "Name"),
			TagType:        attr(se, "TagType"),
			DataType:       attr(se, "DataType"),
			Dimensions:     attr(se, "Dimensions"),
			Radix:          attr(se, "Radix"),
			AliasFor:       attr(se, "AliasFor"),
			Constant:       attr(se, "Constant") == "true",
			ExternalAccess: attr(se, "ExternalAccess"),
			Scope:          scope,
		}
		err := walk(dec, func(se xml.StartElement) error {
			switch se.Name.Local {
			case "Description":
				txt, err := text(dec, se)
				t.Description = txt
				return err
			case "Comments":
				return walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Comment" {
						return skip(dec)
					}
					operand := attr(se, "Operand")
					txt, err := text(dec, se)
					if operand != "" && txt != "" {
						if t.Comments == nil {
							t.Comments = map[string]string{}
						}
						t.Comments[operand] = txt
					}
					return err
				})
			case "Data":
				// Every value is carried twice — once as L5K CDATA and
				// once Decorated. Decorated is the typed one; the L5K
				// copy is redundant (see Normalize's DropL5K).
				if attr(se, "Format") != "Decorated" {
					return skip(dec)
				}
				v, err := parseDecorated(dec)
				t.Value = v
				return err
			default:
				return skip(dec)
			}
		})
		out = append(out, t)
		return err
	})
	return out, err
}

func parseRoutines(dec *xml.Decoder, owner string, lines *lineIndex) ([]*Routine, error) {
	var out []*Routine
	err := walk(dec, func(se xml.StartElement) error {
		if se.Name.Local != "Routine" {
			return skip(dec)
		}
		r := &Routine{
			Name:  attr(se, "Name"),
			Type:  attr(se, "Type"),
			Owner: owner,
			Line:  lines.at(dec.InputOffset()),
		}
		err := walk(dec, func(se xml.StartElement) error {
			switch se.Name.Local {
			case "Description":
				txt, err := text(dec, se)
				r.Description = txt
				return err
			case "RLLContent":
				return walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Rung" {
						return skip(dec)
					}
					rung := Rung{
						Number: atoi(attr(se, "Number")),
						Type:   attr(se, "Type"),
						Line:   lines.at(dec.InputOffset()),
					}
					err := walk(dec, func(se xml.StartElement) error {
						switch se.Name.Local {
						case "Comment":
							txt, err := text(dec, se)
							rung.Comment = txt
							return err
						case "Text":
							txt, err := text(dec, se)
							rung.Text = txt
							return err
						default:
							return skip(dec)
						}
					})
					r.Rungs = append(r.Rungs, rung)
					return err
				})
			case "STContent":
				var b strings.Builder
				err := walk(dec, func(se xml.StartElement) error {
					if se.Name.Local != "Line" {
						return skip(dec)
					}
					txt, err := text(dec, se)
					b.WriteString(txt)
					b.WriteString("\n")
					return err
				})
				r.Text = b.String()
				return err
			default:
				// FBD and SFC bodies are kept verbatim rather than
				// dropped: modelling them is future work, losing them
				// silently would not be.
				raw, err := rawElement(dec, se)
				r.Raw = raw
				return err
			}
		})
		out = append(out, r)
		return err
	})
	return out, err
}

// parseDecorated decodes a <Data Format="Decorated"> body into a Go value:
// bool/int64/float64/string for a scalar, map[string]any for a structure,
// []any for an array.
func parseDecorated(dec *xml.Decoder) (any, error) {
	var out any
	err := walk(dec, func(se xml.StartElement) error {
		v, err := decodedValue(dec, se)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

func decodedValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "DataValue", "DataValueMember", "Element":
		v := scalarValue(attr(se, "DataType"), attr(se, "Radix"), attr(se, "Value"))
		return v, skip(dec)
	case "Structure", "StructureMember":
		// A Logix STRING is a structure of LEN + a SINT array; the useful
		// form of it is the string.
		if s, ok, err := stringStructure(dec, se); ok || err != nil {
			return s, err
		}
		return nil, nil
	case "Array", "ArrayMember":
		var elems []any
		err := walk(dec, func(se xml.StartElement) error {
			v, err := decodedValue(dec, se)
			if err != nil {
				return err
			}
			elems = append(elems, v)
			return nil
		})
		return elems, err
	default:
		return nil, skip(dec)
	}
}

// stringStructure decodes a <Structure>. It returns the decoded members as
// a map, except for a Logix STRING — LEN plus a DATA array of SINT — which
// decodes to the Go string it represents.
func stringStructure(dec *xml.Decoder, se xml.StartElement) (any, bool, error) {
	isString := strings.EqualFold(attr(se, "DataType"), "STRING")
	members := map[string]any{}
	var data string
	err := walk(dec, func(se xml.StartElement) error {
		name := attr(se, "Name")
		if isString && name == "DATA" {
			// The export writes a STRING's DATA as its literal characters
			// when the radix says ASCII, and as per-element SINTs
			// otherwise.
			txt, err := text(dec, se)
			data = decodeASCII(txt)
			return err
		}
		v, err := decodedValue(dec, se)
		if err != nil {
			return err
		}
		if name != "" {
			members[name] = v
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if isString {
		return data, true, nil
	}
	return members, true, nil
}

// decodeASCII turns a Logix ASCII literal ("PUMP$27S") into plain text.
// Logix escapes with '$': $$ is a dollar, $' a quote, $N newline, $T tab,
// and $xx a hex byte.
func decodeASCII(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "'")
	s = strings.TrimSuffix(s, "'")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '$':
			b.WriteByte('$')
		case '\'':
			b.WriteByte('\'')
		case 'N', 'n':
			b.WriteByte('\n')
		case 'R', 'r':
			b.WriteByte('\r')
		case 'T', 't':
			b.WriteByte('\t')
		case 'L', 'l':
			b.WriteByte('\f')
		case 'P', 'p':
			b.WriteByte('\v')
		default:
			if i+1 < len(s) {
				if n, err := strconv.ParseUint(s[i:i+2], 16, 8); err == nil {
					b.WriteByte(byte(n))
					i++
					continue
				}
			}
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// scalarValue types a Value attribute the way the tag store needs it: a
// BOOL is a bool, a Float radix is a float64, everything else an int64.
// Radix Binary/Octal/Hex values carry their base as a 16#/8#/2# prefix.
func scalarValue(dataType, radix, raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	switch strings.ToUpper(dataType) {
	case "BOOL", "BIT":
		return raw != "0" && !strings.EqualFold(raw, "false")
	case "REAL", "LREAL":
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
		return raw
	}
	if strings.EqualFold(radix, "Float") {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	if v, ok := parseRadix(raw); ok {
		return v
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return v
	}
	return raw
}

// parseRadix reads a Logix integer literal: plain decimal, or base#digits
// with the underscore grouping the exporter writes (16#0000_0000).
func parseRadix(raw string) (int64, bool) {
	base := 10
	digits := raw
	if i := strings.Index(raw, "#"); i > 0 {
		b, err := strconv.Atoi(raw[:i])
		if err != nil {
			return 0, false
		}
		base, digits = b, raw[i+1:]
	}
	digits = strings.ReplaceAll(digits, "_", "")
	v, err := strconv.ParseInt(digits, base, 64)
	if err != nil {
		// Logix writes a negative-valued word as its unsigned pattern.
		if u, uerr := strconv.ParseUint(digits, base, 64); uerr == nil {
			return int64(u), true
		}
		return 0, false
	}
	return v, true
}

// --- decoder plumbing ---------------------------------------------------

// walk calls fn for each child StartElement of the element currently open,
// stopping at its EndElement. fn must consume its element — with skip,
// text, or another walk.
//
// EOF before that EndElement means the document is truncated. It is an
// error, not a short read: drift detection compares whole exports, and a
// file that silently parsed as "half a project" would read as a very large
// change rather than as the corruption it is.
func walk(dec *xml.Decoder, fn func(xml.StartElement) error) error {
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(t); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

// skip consumes the element currently open, discarding it.
func skip(dec *xml.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// text consumes the element currently open and returns its character data,
// trimmed. L5X wraps documentation in CDATA, usually on its own line.
func text(dec *xml.Decoder, se xml.StartElement) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err == io.EOF {
			return "", io.ErrUnexpectedEOF
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 1 {
				b.Write(t)
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// rawElement consumes the element currently open and returns it re-encoded,
// so a body the reader does not model yet survives round-tripping.
func rawElement(dec *xml.Decoder, se xml.StartElement) ([]byte, error) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	if err := enc.EncodeToken(se.Copy()); err != nil {
		return nil, err
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
		if depth == 0 {
			break
		}
		if err := enc.EncodeToken(xml.CopyToken(tok)); err != nil {
			return nil, err
		}
	}
	if err := enc.EncodeToken(xml.EndElement{Name: se.Name}); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// lineIndex maps a byte offset in the source to a 1-based line number, so
// routines and rungs report where they came from.
type lineIndex struct{ nl []int }

func newLineIndex(src []byte) *lineIndex {
	var nl []int
	for i, b := range src {
		if b == '\n' {
			nl = append(nl, i)
		}
	}
	return &lineIndex{nl: nl}
}

func (l *lineIndex) at(off int64) int {
	if l == nil {
		return 0
	}
	return sort.SearchInts(l.nl, int(off)) + 1
}
