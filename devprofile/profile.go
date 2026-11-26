// Package devprofile is the device profile format and library: a device
// type described once (who makes it, the documents it was built from, its
// points in the spellings the driver manifests already use, and optionally
// a simulator, an ST FUNCTION_BLOCK that gives it behaviour), for a project
// to declare devices by and for virtdev to simulate them from
// (docs/design/device-library.md, docs/design/virtual-devices.md §3).
//
// The built-in profiles ship in naut (profiles/); a project's own is a file
// ending in .yaml.
package devprofile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Protocols a profile can speak.
const (
	ProtoModbus = "modbus"
	ProtoOPCUA  = "opcua"
)

// Profile is one device type.
type Profile struct {
	// ID names the profile ("asi-275m"): what virtual-devices.yaml's
	// `profile:` refers to.
	ID string `yaml:"profile"`
	// Title is one line for a person ("16-in/16-out Modbus TCP relay board").
	Title string `yaml:"title"`
	Maker string `yaml:"maker"`
	Model string `yaml:"model"`
	// Version is the profile's own revision, bumped when its points or
	// simulator change meaning.
	Version int `yaml:"version"`
	// UDT names the struct type a project's device of this profile is
	// (docs/design/device-library.md §3.2). Profiles whose points are the
	// same may share one. Default: TypeName's spelling of the id.
	UDT string `yaml:"udt"`
	// Docs are the public documents the profile was built from. A point
	// cites one by ID.
	Docs []Doc `yaml:"docs"`
	// Protocol is "modbus" or "opcua".
	Protocol string `yaml:"protocol"`
	// Modbus holds the device's Modbus TCP defaults.
	Modbus *ModbusSpec `yaml:"modbus"`
	// OPCUA holds an OPC UA device's server: port, namespace array, the
	// objects its tree is built from.
	OPCUA *OPCUASpec `yaml:"opcua"`
	// IOLink is an IO-Link master's iolink: block: which of its points
	// hold each port's process data, identity and status
	// (docs/design/iolink.md §3). nil for a device that is not a master.
	IOLink *IOLinkSpec `yaml:"iolink"`
	// Params are values an instance may set and the node columns refer to
	// as {name}: an ifm master's location name prefixes every node id.
	// The map holds each one's default.
	Params map[string]string `yaml:"params"`
	// Points are the device's data, one row per register, coil or node.
	Points []Point `yaml:"points"`
	// Derived are BOOL members computed from a polled point's value (a bit
	// of a status word, a code equal to a value), so an alarm can name a
	// fault. They are never polled or written; see Derived.
	Derived []Derived `yaml:"derived"`
	// Simulator gives the device behaviour; nil means its points hold what
	// was last written (by the controller, or by the process model).
	Simulator *Simulator `yaml:"simulator"`
	// Notes is free text: what is simplified, what is not modelled.
	Notes string `yaml:"notes"`

	// dir is the directory the profile was read from, for its simulator
	// source; "" for a built-in profile (read from the embedded library).
	dir string
	// src is where it came from, for error messages.
	src string
	// file is a project profile's path; "" for a built-in one.
	file string
	// fsys is the file system a vendored profile was read from (ReadFS);
	// dir and file are then paths in it.
	fsys fs.FS
}

// TypeName is the struct type a device of this profile is: the profile's
// udt:, or its id upper-cased with '-' and '.' as '_'
// ("abb-acs355-fmbt21" is ABB_ACS355_FMBT21).
func (p *Profile) TypeName() string {
	if p.UDT != "" {
		return p.UDT
	}
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(p.ID))
}

// Doc is one public document a profile cites.
type Doc struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
	// Code is the publisher's document number ("3AXD50000158515 Rev C").
	Code string `yaml:"code"`
	URL  string `yaml:"url"`
}

// ModbusSpec is a Modbus device's defaults; a virtual-devices.yaml entry can
// override the unit id.
type ModbusSpec struct {
	// Port is the TCP port the device listens on; 0 means 502.
	Port int `yaml:"port"`
	// UnitID is the unit identifier the device answers to.
	UnitID uint8 `yaml:"unit-id"`
	// WordOrder / ByteOrder: "big" (default) or "little", as in the
	// modbus manifest.
	WordOrder string `yaml:"word-order"`
	ByteOrder string `yaml:"byte-order"`
	// Open turns off the address map: every address reads as zero instead
	// of answering exception 02. For a device whose documentation does not
	// say what it refuses.
	Open bool `yaml:"open"`
	// MaxConnections is how many Modbus TCP connections the device
	// documents it accepts at once; 0 means its documentation does not
	// say. A live check of a configuration change (#706) opens a
	// connection of its own, so a device documented at 2 or fewer is
	// checked statically by default, and its virtual twin refuses the
	// connection past the limit.
	MaxConnections int `yaml:"max-connections"`
	// MaxConnectionsDoc cites where the documentation says so, as a
	// point's doc: does ("fmbt21 §7.1, p. 50"). Required with
	// MaxConnections: the default above stands on it.
	MaxConnectionsDoc string `yaml:"max-connections-doc"`
}

// OPCUASpec is an OPC UA device's server.
type OPCUASpec struct {
	// Port is the TCP port; 0 means 4840.
	Port int `yaml:"port"`
	// Namespaces is the server's namespace array from index 1 on, so the
	// node ids' indices land where the device keeps them.
	Namespaces []string `yaml:"namespaces"`
	// Objects names the node ids of the Objects a point's path passes
	// through, by path ("IOLM/Port {n}"). An Object no row names gets a
	// string id of its path in ObjectNamespace.
	Objects []OPCUAObject `yaml:"objects"`
	// ObjectNamespace is the namespace index of Objects no row names;
	// 0 means 1.
	ObjectNamespace int `yaml:"object-namespace"`
}

// OPCUAObject is one Object of an OPC UA device's tree.
type OPCUAObject struct {
	Path   string `yaml:"path"`
	NodeID string `yaml:"node-id"`
	Desc   string `yaml:"desc"`
	Repeat int    `yaml:"repeat"`
	From   *int   `yaml:"from"`
}

// Point is one datum of the device. The protocol columns reuse the driver
// manifests' spellings (modbus.TagBinding: table, address, format, scale,
// offset, writable), so a profile can be rendered into the manifest a
// project's driver reads.
type Point struct {
	// Name is the point's IEC identifier inside the device ("StatusWord").
	Name string `yaml:"name"`
	// Type is its IEC type: BOOL, INT, UINT, WORD, DINT, UDINT, DWORD,
	// REAL, LREAL.
	Type string `yaml:"type"`
	Desc string `yaml:"desc"`
	Unit string `yaml:"unit"`
	// Init is the value the device powers up with; nil means zero.
	Init any `yaml:"init"`
	// Writable says the controller writes it (a command, a reference, a
	// relay). A point that is not writable is the device's to produce.
	Writable bool `yaml:"writable"`
	// Doc cites the document and place this row was taken from:
	// "fmbt21 §7.2, p. 61". The part before the first space is a Docs ID.
	Doc string `yaml:"doc"`
	// Unverified marks a row NOT found in public documentation: a guess
	// kept so the device works, and flagged so nobody mistakes it for fact.
	Unverified bool `yaml:"unverified"`

	// Repeat expands the row into Repeat rows, n = From…From+Repeat-1,
	// substituting {n} in Name, Desc, Doc and the node columns, and
	// advancing the address by Stride (default: the width of one value).
	Repeat int  `yaml:"repeat"`
	From   *int `yaml:"from"`
	Stride int  `yaml:"stride"`

	// OPC UA columns, as opcua.TagBinding names them: the node id ("ns=1;s=
	// IOLM/Port {n}/SIO Input", "ns=3;i=8012"; a repeated numeric id
	// advances by Stride, default 1), and the browse path the node sits at
	// under Objects ("IOLM/Port {n}/SIO Input"): its last element is the
	// BrowseName.
	NodeID string `yaml:"node-id"`
	Path   string `yaml:"path"`
	// Wire is the OPC UA type a STRING point is carried as when it is not
	// String: "ByteString" or "ByteArray" (Byte[]). The point holds the
	// octets as lowercase hex text, so its length varies with the device
	// and the language side stays vendor-neutral (opcua/bytes.go).
	Wire string `yaml:"wire"`

	// Modbus columns.
	Table   string  `yaml:"table"`
	Address uint16  `yaml:"address"`
	Format  string  `yaml:"format"`
	Scale   float64 `yaml:"scale"`
	Offset  float64 `yaml:"offset"`
}

// Simulator names an ST FUNCTION_BLOCK that gives the device behaviour.
// Its VAR_INPUTs named like a writable point receive what the controller
// wrote; its VAR_OUTPUTs named like a read-only point are what the device
// shows. Any other VAR_INPUT or VAR_OUTPUT is a hook for the process model,
// a plant tag named <Device>_<Var>.
type Simulator struct {
	Block  string `yaml:"block"`
	Source string `yaml:"source"`
}

// ParseProfile decodes one profile and checks it. src names it in errors.
func ParseProfile(data []byte, src string) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty profile", src)
		}
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	p.src = src
	if err := p.expand(); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	return &p, nil
}

// IsIdent reports whether s is an IEC identifier, the rule for point and
// device names.
func IsIdent(s string) bool { return identRE.MatchString(s) }

// idRE is a profile id: what profiles/<id>.yaml is named after.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsFileName reports whether s is a plain file name: no directory, no
// "..", not absolute. A profile's simulator sits beside it.
func IsFileName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\:`) && !strings.Contains(s, "..")
}

// IsID reports whether s may be a profile id.
func IsID(s string) bool { return idRE.MatchString(s) && !strings.Contains(s, "..") }

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// expand replaces every repeated row with its copies.
func (p *Profile) expand() error {
	var out []Point
	for _, pt := range p.Points {
		if pt.Repeat == 0 {
			out = append(out, pt)
			continue
		}
		if pt.Repeat < 0 {
			return fmt.Errorf("point %s: repeat must be positive", pt.Name)
		}
		if !strings.Contains(pt.Name, "{n}") {
			return fmt.Errorf("point %s: a repeated row needs {n} in its name", pt.Name)
		}
		from := 1
		if pt.From != nil {
			from = *pt.From
		}
		stride := pt.Stride
		if stride == 0 {
			stride = pt.Words()
		}
		for i := range pt.Repeat {
			n := strconv.Itoa(from + i)
			c := pt
			c.Repeat, c.From, c.Stride = 0, nil, 0
			c.Name = strings.ReplaceAll(pt.Name, "{n}", n)
			c.Desc = strings.ReplaceAll(pt.Desc, "{n}", n)
			c.Doc = strings.ReplaceAll(pt.Doc, "{n}", n)
			c.Path = strings.ReplaceAll(pt.Path, "{n}", n)
			if pt.NodeID != "" {
				id, err := stepNodeID(pt.NodeID, n, i*max(pt.Stride, 1))
				if err != nil {
					return fmt.Errorf("point %s: %w", c.Name, err)
				}
				c.NodeID = id
				out = append(out, c)
				continue
			}
			a := int(pt.Address) + i*stride
			if a > 0xFFFF {
				return fmt.Errorf("point %s: repeat runs past address 65535", c.Name)
			}
			c.Address = uint16(a)
			out = append(out, c)
		}
	}
	p.Points = out
	if p.OPCUA != nil {
		var objs []OPCUAObject
		for _, o := range p.OPCUA.Objects {
			if o.Repeat == 0 {
				objs = append(objs, o)
				continue
			}
			from := 1
			if o.From != nil {
				from = *o.From
			}
			for i := range o.Repeat {
				n := strconv.Itoa(from + i)
				id, err := stepNodeID(o.NodeID, n, i)
				if err != nil {
					return fmt.Errorf("object %s: %w", o.Path, err)
				}
				objs = append(objs, OPCUAObject{Path: strings.ReplaceAll(o.Path, "{n}", n), NodeID: id, Desc: strings.ReplaceAll(o.Desc, "{n}", n)})
			}
		}
		p.OPCUA.Objects = objs
	}
	return nil
}

// stepNodeID is a repeated row's node id: {n} substituted, or, for a
// numeric id with no {n}, the identifier advanced by step.
func stepNodeID(nid, n string, step int) (string, error) {
	if strings.Contains(nid, "{n}") {
		return strings.ReplaceAll(nid, "{n}", n), nil
	}
	i := strings.LastIndex(nid, "i=")
	if i < 0 {
		return "", fmt.Errorf("a repeated node id needs {n}, or a numeric identifier to advance")
	}
	v, err := strconv.ParseUint(nid[i+2:], 10, 32)
	if err != nil {
		return "", fmt.Errorf("node id %s: %w", nid, err)
	}
	return nid[:i+2] + strconv.FormatUint(v+uint64(step), 10), nil
}

func (p *Profile) validate() error {
	if p.ID == "" {
		return fmt.Errorf("profile: an id is required")
	}
	if !IsID(p.ID) {
		return fmt.Errorf("profile: id %q must be letters, digits, '-', '_' and '.' (it names profiles/<id>.yaml)", p.ID)
	}
	if !IsIdent(p.TypeName()) {
		return fmt.Errorf("udt: %q is not an IEC identifier", p.TypeName())
	}
	switch p.Protocol {
	case ProtoModbus:
		if p.Modbus == nil {
			p.Modbus = &ModbusSpec{}
		}
	case ProtoOPCUA:
		if p.OPCUA == nil || len(p.OPCUA.Namespaces) == 0 {
			return fmt.Errorf("opcua: namespaces is required (the namespace array from index 1)")
		}
		if n := p.OPCUA.ObjectNamespace; n < 0 || n > len(p.OPCUA.Namespaces) {
			return fmt.Errorf("opcua: object-namespace %d is not an index of namespaces (1-%d)", n, len(p.OPCUA.Namespaces))
		}
		for _, o := range p.OPCUA.Objects {
			if err := p.checkNamespace(o.NodeID); err != nil {
				return fmt.Errorf("opcua: object %s: %w", o.Path, err)
			}
		}
	default:
		return fmt.Errorf("protocol must be modbus or opcua (got %q)", p.Protocol)
	}
	docs := map[string]bool{}
	for _, d := range p.Docs {
		if d.ID == "" {
			return fmt.Errorf("docs: every document needs an id")
		}
		docs[d.ID] = true
	}
	if m := p.Modbus; m != nil {
		if err := m.checkMaxConnections(docs); err != nil {
			return fmt.Errorf("modbus: %w", err)
		}
	}
	seen := map[string]bool{}
	for i := range p.Points {
		pt := &p.Points[i]
		if !identRE.MatchString(pt.Name) {
			return fmt.Errorf("point %q: the name must be an IEC identifier", pt.Name)
		}
		k := strings.ToLower(pt.Name)
		if seen[k] {
			return fmt.Errorf("point %s: declared twice", pt.Name)
		}
		seen[k] = true
		if pt.Doc != "" {
			id, _, _ := strings.Cut(pt.Doc, " ")
			if !docs[id] {
				return fmt.Errorf("point %s: doc %q is not the id of a document under docs", pt.Name, id)
			}
		} else if !pt.Unverified {
			return fmt.Errorf("point %s: cite a document (doc:) or mark it unverified: true", pt.Name)
		}
		var err error
		if p.Protocol == ProtoOPCUA {
			err = p.validateOPCUA(pt)
		} else {
			err = p.validateModbus(pt)
		}
		if err != nil {
			return fmt.Errorf("point %s: %w", pt.Name, err)
		}
	}
	if err := p.validateDerived(seen); err != nil {
		return err
	}
	if err := p.validateIOLink(); err != nil {
		return err
	}
	if s := p.Simulator; s != nil && (s.Block == "" || s.Source == "") {
		return fmt.Errorf("simulator: block and source are both required")
	}
	if s := p.Simulator; s != nil && !IsFileName(s.Source) {
		return fmt.Errorf("simulator: source %q must be a file name beside the profile, with no directory", s.Source)
	}
	return nil
}

// checkMaxConnections refuses a connection limit the profile does not
// cite, and a citation of no document.
func (m *ModbusSpec) checkMaxConnections(docs map[string]bool) error {
	switch {
	case m.MaxConnections < 0:
		return fmt.Errorf("max-connections %d: want 1 or more (or leave it out when the documentation does not say)", m.MaxConnections)
	case m.MaxConnections == 0 && m.MaxConnectionsDoc != "":
		return fmt.Errorf("max-connections-doc cites a limit max-connections does not give")
	case m.MaxConnections > 0 && m.MaxConnectionsDoc == "":
		return fmt.Errorf("max-connections %d: cite the document that says so (max-connections-doc:); the default of a configuration check stands on it", m.MaxConnections)
	case m.MaxConnections > 0:
		if id, _, _ := strings.Cut(m.MaxConnectionsDoc, " "); !docs[id] {
			return fmt.Errorf("max-connections-doc %q: %q is not the id of a document under docs", m.MaxConnectionsDoc, id)
		}
	}
	return nil
}

// iecTypes are the point types a profile may declare, with the Modbus
// format each defaults to on a register table.
var iecTypes = map[string]string{
	"BOOL": "bool", "INT": "int16", "UINT": "uint16", "WORD": "uint16",
	"DINT": "int32", "UDINT": "uint32", "DWORD": "uint32",
	"REAL": "float32", "LREAL": "float64",
}

// opcuaTypes are the point types an OPC UA device may declare: the Modbus
// set plus the ones OPC UA carries and registers do not.
var opcuaTypes = map[string]bool{
	"BOOL": true, "BYTE": true, "INT": true, "UINT": true, "WORD": true, "DINT": true,
	"UDINT": true, "DWORD": true, "LINT": true, "ULINT": true, "REAL": true, "LREAL": true, "STRING": true,
}

func (p *Profile) validateOPCUA(pt *Point) error {
	pt.Type = strings.ToUpper(pt.Type)
	if !opcuaTypes[pt.Type] {
		return fmt.Errorf("type %q: not one an OPC UA point can be", pt.Type)
	}
	if pt.Table != "" || pt.Format != "" || pt.Scale != 0 || pt.Offset != 0 {
		return fmt.Errorf("an OPC UA point has no Modbus columns")
	}
	if pt.Wire != "" && ((pt.Wire != "ByteString" && pt.Wire != "ByteArray") || pt.Type != "STRING") {
		return fmt.Errorf("wire %q: only a STRING point can be carried as ByteString or ByteArray", pt.Wire)
	}
	if pt.NodeID == "" || pt.Path == "" {
		return fmt.Errorf("an OPC UA point needs node-id and path")
	}
	if err := p.checkNamespace(pt.NodeID); err != nil {
		return err
	}
	return nil
}

func (p *Profile) validateModbus(pt *Point) error {
	if pt.NodeID != "" || pt.Path != "" {
		return fmt.Errorf("a Modbus point has no node-id or path")
	}
	pt.Type = strings.ToUpper(pt.Type)
	def, ok := iecTypes[pt.Type]
	if !ok {
		return fmt.Errorf("type %q: want one of BOOL INT UINT WORD DINT UDINT DWORD REAL LREAL", pt.Type)
	}
	switch pt.Table {
	case "coil", "discrete":
		if pt.Type != "BOOL" {
			return fmt.Errorf("a %s is BOOL", pt.Table)
		}
		if pt.Format != "" && pt.Format != "bool" {
			return fmt.Errorf("a %s takes no format", pt.Table)
		}
		pt.Format = "bool"
	case "holding", "input":
		if pt.Format == "" {
			pt.Format = def
		}
	default:
		return fmt.Errorf("table must be holding, input, coil or discrete (got %q)", pt.Table)
	}
	if pt.Writable && (pt.Table == "input" || pt.Table == "discrete") {
		return fmt.Errorf("the %s table cannot be written", pt.Table)
	}
	if (pt.Scale != 0 || pt.Offset != 0) && pt.Type != "REAL" && pt.Type != "LREAL" {
		return fmt.Errorf("a scaled point is REAL or LREAL")
	}
	if strings.HasPrefix(pt.Format, "bit:") && pt.Type != "BOOL" {
		return fmt.Errorf("format %s is BOOL", pt.Format)
	}
	return nil
}

// Words is how many addresses one value of the row occupies (registers, or
// 1 for a bit), the default stride of a repeated row.
func (pt Point) Words() int {
	f := pt.Format
	if f == "" {
		f = iecTypes[strings.ToUpper(pt.Type)]
	}
	switch f {
	case "int32", "uint32", "float32":
		return 2
	case "float64":
		return 4
	}
	return 1
}

// Point returns the named point, case-insensitively.
func (p *Profile) Point(name string) (Point, bool) {
	for _, pt := range p.Points {
		if strings.EqualFold(pt.Name, name) {
			return pt, true
		}
	}
	return Point{}, false
}
