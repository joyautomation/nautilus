package iodd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// MaxSize is the largest IODD Parse reads. Real IODDs with many
// languages run to a few megabytes.
const MaxSize = 32 << 20

// The XML, as far as nautilus reads it. Element names match in either
// schema namespace; the root's namespace is checked separately.
type xRoot struct {
	XMLName     xml.Name `xml:"IODevice"`
	ProfileBody struct {
		Identity xIdentity `xml:"DeviceIdentity"`
		Function struct {
			Datatypes   []xDatatype `xml:"DatatypeCollection>Datatype"`
			Variables   xVariables  `xml:"VariableCollection"`
			ProcessData []xPD       `xml:"ProcessDataCollection>ProcessData"`
			PDRefs      []xPDRef    `xml:"UserInterface>ProcessDataRefCollection>ProcessDataRef"`
		} `xml:"DeviceFunction"`
	} `xml:"ProfileBody"`
	Texts []xText `xml:"ExternalTextCollection>PrimaryLanguage>Text"`
}

type xIdentity struct {
	VendorID            string     `xml:"vendorId,attr"`
	VendorName          string     `xml:"vendorName,attr"`
	DeviceID            string     `xml:"deviceId,attr"`
	AdditionalDeviceIDs string     `xml:"additionalDeviceIds,attr"`
	DeviceName          xTextRef   `xml:"DeviceName"`
	Variants            []xVariant `xml:"DeviceVariantCollection>DeviceVariant"`
}

type xVariant struct {
	ProductID   string   `xml:"productId,attr"`
	Name        xTextRef `xml:"Name"`
	Description xTextRef `xml:"Description"`
}

type xTextRef struct {
	TextID string `xml:"textId,attr"`
}

type xText struct {
	ID    string `xml:"id,attr"`
	Value string `xml:"value,attr"`
}

// xDatatype is a Datatype, SimpleDatatype or DatatypeCollection entry;
// xsi:type says which type it is.
type xDatatype struct {
	XSIType      string        `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	ID           string        `xml:"id,attr"`
	BitLength    string        `xml:"bitLength,attr"`
	FixedLength  string        `xml:"fixedLength,attr"`
	Encoding     string        `xml:"encoding,attr"`
	RecordItems  []xRecordItem `xml:"RecordItem"`
	SingleValues []xSingle     `xml:"SingleValue"`
	ValueRanges  []xRange      `xml:"ValueRange"`
}

type xRecordItem struct {
	Subindex  string     `xml:"subindex,attr"`
	BitOffset string     `xml:"bitOffset,attr"`
	Simple    *xDatatype `xml:"SimpleDatatype"`
	Ref       *xRef      `xml:"DatatypeRef"`
	Name      xTextRef   `xml:"Name"`
}

type xRef struct {
	DatatypeID string `xml:"datatypeId,attr"`
}

type xSingle struct {
	Value string   `xml:"value,attr"`
	Name  xTextRef `xml:"Name"`
}

type xRange struct {
	Lower string   `xml:"lowerValue,attr"`
	Upper string   `xml:"upperValue,attr"`
	Name  xTextRef `xml:"Name"`
}

type xVariables struct {
	Std []struct {
		ID      string `xml:"id,attr"`
		Default string `xml:"defaultValue,attr"`
	} `xml:"StdVariableRef"`
	Vars []xVariable `xml:"Variable"`
}

type xVariable struct {
	ID       string     `xml:"id,attr"`
	Index    string     `xml:"index,attr"`
	Access   string     `xml:"accessRights,attr"`
	Default  string     `xml:"defaultValue,attr"`
	Datatype *xDatatype `xml:"Datatype"`
	Ref      *xRef      `xml:"DatatypeRef"`
	Name     xTextRef   `xml:"Name"`
}

type xPD struct {
	ID        string `xml:"id,attr"`
	Condition *struct {
		VariableID string `xml:"variableId,attr"`
		Subindex   string `xml:"subindex,attr"`
		Value      string `xml:"value,attr"`
	} `xml:"Condition"`
	In  *xImage `xml:"ProcessDataIn"`
	Out *xImage `xml:"ProcessDataOut"`
}

type xImage struct {
	ID        string     `xml:"id,attr"`
	BitLength string     `xml:"bitLength,attr"`
	Datatype  *xDatatype `xml:"Datatype"`
	Ref       *xRef      `xml:"DatatypeRef"`
	Name      xTextRef   `xml:"Name"`
}

type xPDRef struct {
	ProcessDataID string  `xml:"processDataId,attr"`
	Info          *xInfo  `xml:"ProcessDataInfo"`
	Items         []xInfo `xml:"ProcessDataRecordItemInfo"`
}

type xInfo struct {
	Subindex string `xml:"subindex,attr"`
	Gradient string `xml:"gradient,attr"`
	Offset   string `xml:"offset,attr"`
	UnitCode string `xml:"unitCode,attr"`
}

// Parse reads an IODD. It checks the structure (the root, identity
// ranges, every process data item inside its image, no two items
// overlapping) but not whether the codec can decode every item: that is
// Image.Check, since a device whose process data nautilus cannot decode
// may still be worth reading for its parameters.
func Parse(data []byte) (*IODD, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("%d bytes: larger than an IODD (limit %d)", len(data), MaxSize)
	}
	var x xRoot
	dec := xml.NewDecoder(bytes.NewReader(data))
	// The decoder expands no external entities; a DOCTYPE is refused
	// outright, since an IODD has none.
	if err := checkNoDoctype(data); err != nil {
		return nil, err
	}
	if err := dec.Decode(&x); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("not an IODD: empty document")
		}
		return nil, fmt.Errorf("not an IODD: %w", err)
	}
	d := &IODD{}
	switch x.XMLName.Space {
	case NS11:
		d.Version = "1.1"
	case NS101:
		d.Version = "1.0.1"
	default:
		return nil, fmt.Errorf("not an IODD 1.0.1 or 1.1: the IODevice element's namespace is %q", x.XMLName.Space)
	}
	p := &parser{texts: map[string]string{}, types: map[string]*xDatatype{}}
	for _, t := range x.Texts {
		p.texts[t.ID] = t.Value
	}
	fn := &x.ProfileBody.Function
	for i := range fn.Datatypes {
		dt := &fn.Datatypes[i]
		if dt.ID != "" {
			p.types[dt.ID] = dt
		}
	}
	if err := p.identity(&d.Identity, &x.ProfileBody.Identity); err != nil {
		return nil, err
	}
	scale := map[string]xPDRef{}
	for _, r := range fn.PDRefs {
		scale[r.ProcessDataID] = r
	}
	for _, xpd := range fn.ProcessData {
		pd := ProcessData{ID: xpd.ID}
		if c := xpd.Condition; c != nil {
			v, err := strconv.ParseInt(strings.TrimSpace(c.Value), 0, 64)
			if err != nil {
				return nil, fmt.Errorf("process data %s: condition value %q is not an integer", xpd.ID, c.Value)
			}
			sub, err := optInt(c.Subindex)
			if err != nil {
				return nil, fmt.Errorf("process data %s: condition subindex %q: %w", xpd.ID, c.Subindex, err)
			}
			pd.Condition = &Condition{VariableID: c.VariableID, Subindex: sub, Value: v}
		}
		var err error
		if pd.In, err = p.image("in", xpd.In, scale); err != nil {
			return nil, fmt.Errorf("process data %s: %w", xpd.ID, err)
		}
		if pd.Out, err = p.image("out", xpd.Out, scale); err != nil {
			return nil, fmt.Errorf("process data %s: %w", xpd.ID, err)
		}
		d.ProcessData = append(d.ProcessData, pd)
	}
	for _, s := range fn.Variables.Std {
		v := Variable{ID: s.ID, Default: s.Default, Standard: true, Index: -1}
		if std, ok := standardVariables[s.ID]; ok {
			v.Index, v.Access, v.Name, v.Type = std.index, std.access, std.name, std.typ
		}
		d.Variables = append(d.Variables, v)
	}
	for _, xv := range fn.Variables.Vars {
		v, err := p.variable(xv)
		if err != nil {
			return nil, fmt.Errorf("variable %s: %w", xv.ID, err)
		}
		d.Variables = append(d.Variables, v)
	}
	return d, nil
}

// checkNoDoctype refuses a document type declaration before the root:
// IODDs carry none, and refusing it keeps entity games out entirely.
func checkNoDoctype(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return nil // Decode reports it
		}
		switch t := tok.(type) {
		case xml.Directive:
			if bytes.HasPrefix(bytes.TrimSpace(t), []byte("DOCTYPE")) {
				return fmt.Errorf("not an IODD: it has a DOCTYPE, which an IODD never has")
			}
		case xml.StartElement:
			return nil
		}
	}
}

type parser struct {
	texts map[string]string
	types map[string]*xDatatype
}

func (p *parser) text(r xTextRef) string { return p.texts[r.TextID] }

func (p *parser) identity(id *Identity, x *xIdentity) error {
	v, err := strconv.ParseUint(strings.TrimSpace(x.VendorID), 0, 32)
	if err != nil || v < 1 || v > 0xFFFF {
		return fmt.Errorf("DeviceIdentity: vendorId %q is not a vendor id (1..65535)", x.VendorID)
	}
	d, err := strconv.ParseUint(strings.TrimSpace(x.DeviceID), 0, 32)
	if err != nil || d < 1 || d > 0xFFFFFF {
		return fmt.Errorf("DeviceIdentity: deviceId %q is not a device id (1..16777215)", x.DeviceID)
	}
	id.VendorID, id.DeviceID = uint32(v), uint32(d)
	for f := range strings.FieldsSeq(x.AdditionalDeviceIDs) {
		a, err := strconv.ParseUint(f, 0, 32)
		if err != nil || a < 1 || a > 0xFFFFFF {
			return fmt.Errorf("DeviceIdentity: additionalDeviceIds %q is not a device id", f)
		}
		id.AdditionalDeviceIDs = append(id.AdditionalDeviceIDs, uint32(a))
	}
	id.VendorName = x.VendorName
	id.DeviceName = p.text(x.DeviceName)
	for _, xv := range x.Variants {
		id.Variants = append(id.Variants, Variant{ProductID: xv.ProductID, Name: p.text(xv.Name), Description: p.text(xv.Description)})
	}
	return nil
}

// resolve follows a DatatypeRef to the collection's entry.
func (p *parser) resolve(dt *xDatatype, ref *xRef) (*xDatatype, error) {
	if dt != nil {
		return dt, nil
	}
	if ref == nil {
		return nil, fmt.Errorf("no Datatype or DatatypeRef")
	}
	r := p.types[ref.DatatypeID]
	if r == nil {
		return nil, fmt.Errorf("DatatypeRef %q names no datatype in the DatatypeCollection", ref.DatatypeID)
	}
	return r, nil
}

func (p *parser) image(dir string, x *xImage, scale map[string]xPDRef) (*Image, error) {
	if x == nil {
		return nil, nil
	}
	where := "ProcessData" + map[string]string{"in": "In", "out": "Out"}[dir] + " " + x.ID
	bits, err := strconv.Atoi(strings.TrimSpace(x.BitLength))
	if err != nil || bits < 1 || bits > 256 {
		return nil, fmt.Errorf("%s: bitLength %q is not 1..256", where, x.BitLength)
	}
	im := &Image{ID: x.ID, Name: p.text(x.Name), BitLength: bits}
	dt, err := p.resolve(x.Datatype, x.Ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if typeName(dt) == "RecordT" {
		for _, ri := range dt.RecordItems {
			it, err := p.recordItem(ri)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			im.Items = append(im.Items, it)
		}
	} else {
		t, err := p.simple(dt)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		im.Items = []Item{{Name: im.Name, Type: t}}
	}
	if err := im.checkLayout(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if r, ok := scale[x.ID]; ok {
		if err := applyScale(im, r); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
	}
	return im, nil
}

func (p *parser) recordItem(ri xRecordItem) (Item, error) {
	sub, err := strconv.Atoi(strings.TrimSpace(ri.Subindex))
	if err != nil || sub < 1 || sub > 255 {
		return Item{}, fmt.Errorf("RecordItem subindex %q is not 1..255", ri.Subindex)
	}
	off, err := strconv.Atoi(strings.TrimSpace(ri.BitOffset))
	if err != nil || off < 0 {
		return Item{}, fmt.Errorf("RecordItem %d: bitOffset %q is not a bit offset", sub, ri.BitOffset)
	}
	dt, err := p.resolve(ri.Simple, ri.Ref)
	if err != nil {
		return Item{}, fmt.Errorf("RecordItem %d: %w", sub, err)
	}
	t, err := p.simple(dt)
	if err != nil {
		return Item{}, fmt.Errorf("RecordItem %d: %w", sub, err)
	}
	return Item{Subindex: sub, BitOffset: off, Name: p.text(ri.Name), Type: t}, nil
}

func typeName(dt *xDatatype) string {
	t := dt.XSIType
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		t = t[i+1:]
	}
	return t
}

// simple reads a simple datatype. A type the codec does not decode is
// returned as Unsupported, not refused: Image.Check refuses it where it
// matters.
func (p *parser) simple(dt *xDatatype) (Type, error) {
	name := typeName(dt)
	t := Type{Name: name}
	for _, s := range dt.SingleValues {
		t.Values = append(t.Values, NamedValue{Value: s.Value, Name: p.text(s.Name)})
	}
	for _, r := range dt.ValueRanges {
		t.Ranges = append(t.Ranges, Range{Lower: r.Lower, Upper: r.Upper, Name: p.text(r.Name)})
	}
	bitLength := func(lo, hi int) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(dt.BitLength))
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%s: bitLength %q is not %d..%d", name, dt.BitLength, lo, hi)
		}
		return n, nil
	}
	fixed := func() (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(dt.FixedLength))
		if err != nil || n < 1 || n > 232 {
			return 0, fmt.Errorf("%s: fixedLength %q is not 1..232", name, dt.FixedLength)
		}
		return n, nil
	}
	var err error
	switch name {
	case "BooleanT":
		t.Kind, t.BitLength = Boolean, 1
	case "UIntegerT":
		t.Kind = UInteger
		t.BitLength, err = bitLength(1, 64)
	case "IntegerT":
		t.Kind = Integer
		t.BitLength, err = bitLength(2, 64)
	case "Float32T":
		t.Kind, t.BitLength = Float32, 32
	case "StringT":
		t.Kind = String
		if t.FixedLength, err = fixed(); err == nil {
			t.BitLength = 8 * t.FixedLength
		}
		switch t.Encoding = dt.Encoding; t.Encoding {
		case "", "US-ASCII":
			t.Encoding = "US-ASCII"
		case "UTF-8":
		default:
			return Type{}, fmt.Errorf("StringT: encoding %q is not US-ASCII or UTF-8", dt.Encoding)
		}
	case "OctetStringT":
		t.Kind = OctetString
		if t.FixedLength, err = fixed(); err == nil {
			t.BitLength = 8 * t.FixedLength
		}
	case "":
		return Type{}, fmt.Errorf("a datatype with no xsi:type")
	default:
		t.Kind = Unsupported
		if n, e := strconv.Atoi(strings.TrimSpace(dt.BitLength)); e == nil {
			t.BitLength = n
		}
	}
	return t, err
}

func (p *parser) variable(xv xVariable) (Variable, error) {
	idx, err := strconv.Atoi(strings.TrimSpace(xv.Index))
	if err != nil || idx < 0 || idx > 0xFFFF {
		return Variable{}, fmt.Errorf("index %q is not 0..65535", xv.Index)
	}
	switch xv.Access {
	case "ro", "wo", "rw":
	default:
		return Variable{}, fmt.Errorf("accessRights %q is not ro, wo or rw", xv.Access)
	}
	v := Variable{ID: xv.ID, Index: idx, Access: xv.Access, Default: xv.Default, Name: p.text(xv.Name)}
	dt, err := p.resolve(xv.Datatype, xv.Ref)
	if err != nil {
		return Variable{}, err
	}
	if typeName(dt) == "RecordT" {
		v.Type = Type{Name: "RecordT"}
		for _, ri := range dt.RecordItems {
			it, err := p.recordItem(ri)
			if err != nil {
				return Variable{}, err
			}
			v.Items = append(v.Items, it)
		}
		return v, nil
	}
	v.Type, err = p.simple(dt)
	return v, err
}

// checkLayout checks that every item lies inside the image and that no
// two items share a bit or a subindex.
func (im *Image) checkLayout() error {
	type span struct{ lo, hi, sub int }
	var spans []span
	subs := map[int]bool{}
	for _, it := range im.Items {
		if it.Subindex != 0 {
			if subs[it.Subindex] {
				return fmt.Errorf("subindex %d is declared twice", it.Subindex)
			}
			subs[it.Subindex] = true
		}
		w := it.Type.BitLength
		if w == 0 {
			continue // an unsupported type of unknown width; Check refuses it
		}
		if it.BitOffset+w > im.BitLength {
			return fmt.Errorf("%s: bits %d..%d lie outside the %d-bit image", it.label(), it.BitOffset, it.BitOffset+w-1, im.BitLength)
		}
		spans = append(spans, span{it.BitOffset, it.BitOffset + w, it.Subindex})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
	for i := 1; i < len(spans); i++ {
		if spans[i].lo < spans[i-1].hi {
			return fmt.Errorf("record items %d and %d overlap", spans[i-1].sub, spans[i].sub)
		}
	}
	return nil
}

func (it Item) label() string {
	if it.Subindex == 0 {
		return "the value"
	}
	if it.Name != "" {
		return fmt.Sprintf("record item %d (%s)", it.Subindex, it.Name)
	}
	return fmt.Sprintf("record item %d", it.Subindex)
}

func applyScale(im *Image, r xPDRef) error {
	set := func(it *Item, in xInfo) error {
		if in.Gradient != "" {
			g, err := strconv.ParseFloat(strings.TrimSpace(in.Gradient), 64)
			if err != nil || g == 0 {
				return fmt.Errorf("%s: gradient %q is not a non-zero number", it.label(), in.Gradient)
			}
			it.Gradient, it.Scaled = g, true
		}
		if in.Offset != "" {
			o, err := strconv.ParseFloat(strings.TrimSpace(in.Offset), 64)
			if err != nil {
				return fmt.Errorf("%s: offset %q is not a number", it.label(), in.Offset)
			}
			it.Offset, it.Scaled = o, true
		}
		if in.UnitCode != "" {
			u, err := strconv.Atoi(strings.TrimSpace(in.UnitCode))
			if err != nil || u < 0 {
				return fmt.Errorf("%s: unitCode %q is not a unit code", it.label(), in.UnitCode)
			}
			it.UnitCode = u
		}
		if it.Scaled && in.Gradient == "" {
			it.Gradient = 1
		}
		if it.Scaled && it.Type.Kind != UInteger && it.Type.Kind != Integer {
			return fmt.Errorf("%s: gradient or offset on a %s; only integers are scaled", it.label(), it.Type.Name)
		}
		return nil
	}
	if r.Info != nil && len(im.Items) == 1 && im.Items[0].Subindex == 0 {
		if err := set(&im.Items[0], *r.Info); err != nil {
			return err
		}
	}
	for _, in := range r.Items {
		sub, err := strconv.Atoi(strings.TrimSpace(in.Subindex))
		if err != nil {
			return fmt.Errorf("ProcessDataRecordItemInfo subindex %q is not a number", in.Subindex)
		}
		found := false
		for i := range im.Items {
			if im.Items[i].Subindex == sub {
				if err := set(&im.Items[i], in); err != nil {
					return err
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("ProcessDataRecordItemInfo names subindex %d, which the record does not have", sub)
		}
	}
	return nil
}

func optInt(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return strconv.Atoi(strings.TrimSpace(s))
}
