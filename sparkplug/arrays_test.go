// arrays_test.go covers the Sparkplug array datatypes end to end on the edge
// side: the bytes_value wire format against the spec's own examples (and
// Tahu's reading of the ones the spec leaves to them), round trips for every
// array datatype at its edges, the ir mapping both ways, and a node's birth
// and data carrying ARRAY tags and ARRAY template members.

package sparkplug

import (
	"bytes"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// wireBytes encodes one metric and returns its bytes_value as it went out.
func wireBytes(t *testing.T, dt spb.DataType, v any) []byte {
	t.Helper()
	b, err := Payload{Metrics: []Metric{{Name: "a", Datatype: dt, Value: v}}}.Encode()
	if err != nil {
		t.Fatalf("encode %v: %v", dt, err)
	}
	var raw spb.Payload
	if err := unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	bv, ok := raw.Metrics[0].GetValue().(*spb.Payload_Metric_BytesValue)
	if !ok {
		t.Fatalf("%v went out as %T, want bytes_value", dt, raw.Metrics[0].GetValue())
	}
	return bv.BytesValue
}

// TestArrayWireFormatSpecExamples pins the encoding to Sparkplug 3.0
// §6.4.17's "Array Types" examples. Where the spec's printed example
// contradicts its own text (FloatArray, DoubleArray and DateTimeArray
// examples are not little-endian / not 8 bytes per element), the text and
// Eclipse Tahu's Java encoder win, and the vector here is computed from them.
func TestArrayWireFormatSpecExamples(t *testing.T) {
	u64 := uint64(16444743074749521625)
	cases := []struct {
		name string
		dt   spb.DataType
		v    any
		want []byte
	}{
		{"Int32 intro", spb.DataType_Int32Array, []int64{123456789, 987654321},
			[]byte{0x15, 0xCD, 0x5B, 0x07, 0xB1, 0x68, 0xDE, 0x3A}},
		// The spec prints [-23, 123] → [0xEF, 0x7B], but -23 is 0xE9 in two's
		// complement (0xEF is -17): an erratum in the example, not the rule.
		{"Int8", spb.DataType_Int8Array, []int64{-23, 123}, []byte{0xE9, 0x7B}},
		{"Int16", spb.DataType_Int16Array, []int64{-30000, 30000}, []byte{0xD0, 0x8A, 0x30, 0x75}},
		{"Int32", spb.DataType_Int32Array, []int64{-1, 315338746},
			[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFA, 0xAF, 0xCB, 0x12}},
		{"Int64", spb.DataType_Int64Array, []int64{-4270929666821191986, -3601064768563266876},
			[]byte{0xCE, 0x06, 0x72, 0xAC, 0x18, 0x9C, 0xBA, 0xC4, 0xC4, 0xBA, 0x9C, 0x18, 0xAC, 0x72, 0x06, 0xCE}},
		{"UInt8", spb.DataType_UInt8Array, []int64{23, 250}, []byte{0x17, 0xFA}},
		{"UInt16", spb.DataType_UInt16Array, []int64{30, 52360}, []byte{0x1E, 0x00, 0x88, 0xCC}},
		{"UInt32", spb.DataType_UInt32Array, []int64{52, 3293969225},
			[]byte{0x34, 0x00, 0x00, 0x00, 0x49, 0xFB, 0x55, 0xC4}},
		{"UInt64", spb.DataType_UInt64Array, []int64{52, int64(u64)},
			[]byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0xD9, 0x9E, 0x02, 0xD1, 0xB2, 0x76, 0x37, 0xE4}},
		// 1.23f = 0x3F9D70A4, 89.341f = 0x42B2AE98: little-endian per the text.
		{"Float", spb.DataType_FloatArray, []float64{1.23, 89.341},
			[]byte{0xA4, 0x70, 0x9D, 0x3F, 0x98, 0xAE, 0xB2, 0x42}},
		{"Double", spb.DataType_DoubleArray, []float64{12.354213},
			leU64(math.Float64bits(12.354213))},
		// [f,f,t,t,f,t,f,f,t,t,f,t] → count 12 LE, then MSB-first bits:
		// 0011 0100 = 0x34, 1101 xxxx = 0xD0 with the padding zeroed.
		{"Boolean", spb.DataType_BooleanArray,
			[]bool{false, false, true, true, false, true, false, false, true, true, false, true},
			[]byte{0x0C, 0x00, 0x00, 0x00, 0x34, 0xD0}},
		{"String", spb.DataType_StringArray, []string{"ABC", "hello"},
			[]byte{0x41, 0x42, 0x43, 0x00, 0x68, 0x65, 0x6c, 0x6c, 0x6f, 0x00}},
		{"DateTime", spb.DataType_DateTimeArray, []int64{1256102875335, 1656107875000},
			append(leU64(1256102875335), leU64(1656107875000)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wireBytes(t, c.dt, c.v); !bytes.Equal(got, c.want) {
				t.Errorf("bytes_value = % X, want % X", got, c.want)
			}
		})
	}
}

func leU64(x uint64) []byte {
	b := make([]byte, 8)
	for i := range b {
		b[i] = byte(x >> (8 * i))
	}
	return b
}

// TestArrayRoundTrips sends every array datatype through Encode/Decode at
// its edges: empty, one element, the type's extremes, and BooleanArrays
// whose length is and is not a multiple of 8.
func TestArrayRoundTrips(t *testing.T) {
	bools := func(n int) []bool {
		out := make([]bool, n)
		for i := range out {
			out[i] = i%3 == 0 || i == n-1
		}
		return out
	}
	cases := []struct {
		name string
		dt   spb.DataType
		v    any
	}{
		{"Int8 empty", spb.DataType_Int8Array, []int64{}},
		{"Int8 single", spb.DataType_Int8Array, []int64{-128}},
		{"Int8 extremes", spb.DataType_Int8Array, []int64{-128, 0, 127}},
		{"Int16 extremes", spb.DataType_Int16Array, []int64{math.MinInt16, -1, math.MaxInt16}},
		{"Int32 extremes", spb.DataType_Int32Array, []int64{math.MinInt32, -1, math.MaxInt32}},
		{"Int32 empty", spb.DataType_Int32Array, []int64{}},
		{"Int64 extremes", spb.DataType_Int64Array, []int64{math.MinInt64, -1, math.MaxInt64}},
		{"UInt8 extremes", spb.DataType_UInt8Array, []int64{0, 255}},
		{"UInt16 extremes", spb.DataType_UInt16Array, []int64{0, math.MaxUint16}},
		{"UInt32 extremes", spb.DataType_UInt32Array, []int64{0, math.MaxUint32}},
		{"UInt64 wraps like the scalar", spb.DataType_UInt64Array, []int64{0, -1}},
		{"Float single", spb.DataType_FloatArray, []float64{0.5}},
		{"Float empty", spb.DataType_FloatArray, []float64{}},
		{"Double extremes", spb.DataType_DoubleArray, []float64{-math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1)}},
		{"Boolean empty", spb.DataType_BooleanArray, []bool{}},
		{"Boolean single", spb.DataType_BooleanArray, []bool{true}},
		{"Boolean 7", spb.DataType_BooleanArray, bools(7)},
		{"Boolean 8", spb.DataType_BooleanArray, bools(8)},
		{"Boolean 9", spb.DataType_BooleanArray, bools(9)},
		{"Boolean 17", spb.DataType_BooleanArray, bools(17)},
		{"String empty", spb.DataType_StringArray, []string{}},
		{"String single empty string", spb.DataType_StringArray, []string{""}},
		{"String with empties", spb.DataType_StringArray, []string{"", "a", "", "ünïcødé", ""}},
		{"DateTime single", spb.DataType_DateTimeArray, []int64{1700000000000}},
		{"DateTime empty", spb.DataType_DateTimeArray, []int64{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := Payload{Metrics: []Metric{{Name: "a", Datatype: c.dt, Value: c.v}}}.Encode()
			if err != nil {
				t.Fatal(err)
			}
			p, err := DecodePayload(b)
			if err != nil {
				t.Fatal(err)
			}
			m := p.Metrics[0]
			if m.Datatype != c.dt || m.IsNull || m.decodeErr != nil {
				t.Fatalf("decoded %+v", m)
			}
			if !reflect.DeepEqual(m.Value, c.v) {
				t.Errorf("round trip = %#v, want %#v", m.Value, c.v)
			}
		})
	}
	// FloatArray is binary32 on the wire: a float64 that is not exactly a
	// float32 comes back as the nearest float32, the same as a Float scalar.
	b, _ := Payload{Metrics: []Metric{{Name: "a", Datatype: spb.DataType_FloatArray, Value: []float64{1.23}}}}.Encode()
	p, _ := DecodePayload(b)
	if got := p.Metrics[0].Value.([]float64)[0]; got != float64(float32(1.23)) {
		t.Errorf("FloatArray 1.23 → %v, want float32(1.23)", got)
	}
}

// TestArrayEncodeRefusesWhatDoesNotFit — an element out of its datatype's
// range, or a string the terminator would split, is an error at encode, never
// a silent truncation on the wire.
func TestArrayEncodeRefusesWhatDoesNotFit(t *testing.T) {
	cases := []struct {
		name string
		dt   spb.DataType
		v    any
		want string
	}{
		{"Int8 overflow", spb.DataType_Int8Array, []int64{1, 300}, "element 1 (300) does not fit"},
		{"Int16 underflow", spb.DataType_Int16Array, []int64{math.MinInt16 - 1}, "does not fit"},
		{"UInt16 negative", spb.DataType_UInt16Array, []int64{-1}, "does not fit"},
		{"UInt32 overflow", spb.DataType_UInt32Array, []int64{math.MaxUint32 + 1}, "does not fit"},
		{"String NUL", spb.DataType_StringArray, []string{"a\x00b"}, "NUL"},
		{"wrong slice", spb.DataType_Int32Array, []float64{1}, "expected []int64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Payload{Metrics: []Metric{{Name: "a", Datatype: c.dt, Value: c.v}}}.Encode()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

// rawArrayMetric hand-builds a received metric with an arbitrary bytes_value.
func rawArrayMetric(t *testing.T, dt spb.DataType, b []byte) Metric {
	t.Helper()
	pb := &spb.Payload_Metric{Datatype: ptrU32(uint32(dt)), Name: ptrStr("a")}
	if b != nil {
		pb.Value = &spb.Payload_Metric_BytesValue{BytesValue: b}
	}
	return decodeMetric(pb)
}

func ptrU32(v uint32) *uint32 { return &v }
func ptrStr(v string) *string { return &v }

// TestArrayDecodeMalformed — a bytes_value that is not a whole number of
// elements, or a BooleanArray whose count overruns its bits, decodes to an
// error ValueFromMetric reports; it does not fail the payload, and is not
// guessed at.
func TestArrayDecodeMalformed(t *testing.T) {
	for _, c := range []struct {
		name string
		dt   spb.DataType
		b    []byte
	}{
		{"Int32 ragged", spb.DataType_Int32Array, []byte{1, 2, 3}},
		{"Double ragged", spb.DataType_DoubleArray, make([]byte, 12)},
		{"Boolean short count", spb.DataType_BooleanArray, []byte{1, 0}},
		{"Boolean overrun", spb.DataType_BooleanArray, []byte{9, 0, 0, 0, 0xFF}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := rawArrayMetric(t, c.dt, c.b)
			if m.decodeErr == nil {
				t.Fatalf("decoded %#v with no error", m.Value)
			}
			if _, err := ValueFromMetric(m, nil); err == nil {
				t.Error("ValueFromMetric accepted a malformed array")
			}
		})
	}
	// Leniency where it costs nothing: no bytes_value at all is an empty
	// array (Tahu's reading), and a final string missing its terminator is
	// kept.
	if m := rawArrayMetric(t, spb.DataType_Int32Array, nil); m.decodeErr != nil || !reflect.DeepEqual(m.Value, []int64{}) {
		t.Errorf("absent bytes_value = %#v, %v; want an empty array", m.Value, m.decodeErr)
	}
	if m := rawArrayMetric(t, spb.DataType_StringArray, []byte("ab\x00cd")); !reflect.DeepEqual(m.Value, []string{"ab", "cd"}) {
		t.Errorf("unterminated tail = %#v", m.Value)
	}
}

func intArr(xs ...int64) ir.Value {
	v := ir.Value{Kind: ir.TypeArray, Arr: make([]ir.Value, len(xs))}
	for i, x := range xs {
		v.Arr[i] = ir.IntVal(x)
	}
	return v
}

// TestMetricFromValueArrays — the ir → Sparkplug mapping is the scalar one,
// element-wise: integers (and TIME) as Int64Array, REAL as DoubleArray.
func TestMetricFromValueArrays(t *testing.T) {
	reals := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.RealVal(1.5), ir.RealVal(-2)}}
	bools := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.BoolVal(true), ir.BoolVal(false)}}
	strs := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.StringVal("a"), ir.StringVal("")}}
	times := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.TimeVal(1500)}}
	cases := []struct {
		name string
		v    ir.Value
		dt   spb.DataType
		want any
	}{
		{"DINT array", intArr(2026, 10, 7, 23, 59, 1, 0), spb.DataType_Int64Array, []int64{2026, 10, 7, 23, 59, 1, 0}},
		{"REAL array", reals, spb.DataType_DoubleArray, []float64{1.5, -2}},
		{"BOOL array", bools, spb.DataType_BooleanArray, []bool{true, false}},
		{"STRING array", strs, spb.DataType_StringArray, []string{"a", ""}},
		{"TIME array", times, spb.DataType_Int64Array, []int64{1500}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := MetricFromValue("x", c.v, "")
			if err != nil {
				t.Fatal(err)
			}
			if m.Datatype != c.dt || !reflect.DeepEqual(m.Value, c.want) {
				t.Errorf("got %v %#v, want %v %#v", m.Datatype, m.Value, c.dt, c.want)
			}
		})
	}

	empty := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{}}
	if _, err := MetricFromValue("e", empty, ""); err == nil {
		t.Error("an empty array with no declared type published — its datatype would be a guess")
	}
	m, err := MetricFromTypedValue("e", empty, &ir.Type{Kind: ir.TypeArray, Elem: ir.BoolT}, "")
	if err != nil || m.Datatype != spb.DataType_BooleanArray {
		t.Errorf("typed empty array = %v, %v; want BooleanArray", m.Datatype, err)
	}

	sd := motorDef()
	structs := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: sd})}}
	if _, err := MetricFromValue("s", structs, ""); err == nil {
		t.Error("an array of structs published; Sparkplug has no array of templates")
	}
	nested := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{intArr(1)}}
	if _, err := MetricFromValue("n", nested, ""); err == nil {
		t.Error("a nested array published")
	}
	mixed := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.IntVal(1), ir.StringVal("x")}}
	if _, err := MetricFromValue("m", mixed, ""); err == nil {
		t.Error("a mixed-kind array published")
	}
}

// TestValueFromMetricArrays — the host-side inverse: arrays decode to ir
// arrays, shaped and length-checked by the target.
func TestValueFromMetricArrays(t *testing.T) {
	m := Metric{Name: "gettime", Datatype: spb.DataType_Int32Array, Value: []int64{2026, 10, 7, 23, 59, 1, 0}}
	dint7 := &ir.Type{Kind: ir.TypeArray, Elem: ir.IntT, ArrLen: 7}
	got, err := ValueFromMetric(m, dint7)
	if err != nil {
		t.Fatal(err)
	}
	if want := intArr(2026, 10, 7, 23, 59, 1, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Untyped: the natural mapping.
	got, err = ValueFromMetric(Metric{Name: "b", Datatype: spb.DataType_BooleanArray, Value: []bool{true}}, nil)
	if err != nil || got.Kind != ir.TypeArray || len(got.Arr) != 1 || !got.Arr[0].B {
		t.Errorf("untyped BooleanArray = %+v, %v", got, err)
	}

	// Length is the contract.
	short := Metric{Name: "gettime", Datatype: spb.DataType_Int32Array, Value: []int64{1, 2, 3}}
	if _, err := ValueFromMetric(short, dint7); err == nil || !strings.Contains(err.Error(), "3 elements, the target array has 7") {
		t.Errorf("short array: err = %v", err)
	}

	// Elements coerce to the declared element kind (an Int32Array into a
	// REAL array), as scalars coerce.
	real2 := &ir.Type{Kind: ir.TypeArray, Elem: ir.RealT, ArrLen: 2}
	got, err = ValueFromMetric(Metric{Name: "r", Datatype: spb.DataType_Int32Array, Value: []int64{3, 4}}, real2)
	if err != nil || got.Arr[0].Kind != ir.TypeReal || got.Arr[1].F != 4 {
		t.Errorf("coerced = %+v, %v", got, err)
	}

	// An array into a scalar target is a shape error.
	if _, err := ValueFromMetric(m, ir.IntT); err == nil {
		t.Error("an array decoded into a scalar target")
	}
}

// clockDef is a Get_TOD-shaped struct: an ARRAY [0..6] OF DINT member beside
// a scalar.
func clockDef() *ir.StructDef {
	return &ir.StructDef{
		Name: "Get_TOD",
		Fields: []ir.StructField{
			{Name: "EnableIn", Type: ir.BoolT},
			{Name: "gettime", Type: &ir.Type{Kind: ir.TypeArray, Elem: ir.IntT, ArrLen: 7}},
		},
		FieldIndex: map[string]int{"EnableIn": 0, "gettime": 1},
	}
}

// TestTemplateArrayMemberDefinitionAndInstance — an array member's datatype
// is the array datatype in the definition (no value, like every definition
// member) and in the instance (with its elements); the host rebuilds the
// right struct from the definition and decodes the instance into it.
func TestTemplateArrayMemberDefinitionAndInstance(t *testing.T) {
	sd := clockDef()
	def := definitionTemplate(sd)
	if len(def.Metrics) != 2 {
		t.Fatalf("definition members %+v", def.Metrics)
	}
	if g := def.Metrics[1]; g.Name != "gettime" || g.Datatype != spb.DataType_Int64Array || !g.IsNull {
		t.Errorf("definition member = %+v, want gettime Int64Array null", g)
	}

	v := ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: sd})
	v.Fld[0] = ir.BoolVal(true)
	v.Fld[1] = intArr(2026, 10, 7, 23, 59, 1, 0)
	inst, err := MetricFromValue("RTC", v, sd.Name)
	if err != nil {
		t.Fatal(err)
	}

	// Through the wire, as a host receives it.
	b, err := Payload{Metrics: []Metric{
		{Name: sd.Name, Datatype: spb.DataType_Template, Value: def},
		inst,
	}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodePayload(b)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := StructDefsFromTemplates(p.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	gotSD := defs["Get_TOD"]
	if gotSD == nil || gotSD.Fields[1].Type.Kind != ir.TypeArray || gotSD.Fields[1].Type.Elem.Kind != ir.TypeInt {
		t.Fatalf("rebuilt StructDef = %+v", gotSD)
	}
	got, err := ValueFromMetric(p.Metrics[1], &ir.Type{Kind: ir.TypeStruct, Struct: gotSD})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fld[0].B || !reflect.DeepEqual(got.Fld[1], intArr(2026, 10, 7, 23, 59, 1, 0)) {
		t.Errorf("decoded instance = %+v", got)
	}

	// Into the edge's own declared StructDef (length 7) a 7-element member
	// fits; a partial template carrying only gettime keeps EnableIn.
	partial := Metric{Name: "RTC", Datatype: spb.DataType_Template, Value: &Template{TemplateRef: "Get_TOD",
		Metrics: []Metric{{Name: "gettime", Datatype: spb.DataType_Int32Array, Value: []int64{1, 2, 3, 4, 5, 6, 7}}}}}
	merged, _, err := ValueFromMetricInto(partial, v)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Fld[0].B || merged.Fld[1].Arr[6].I != 7 {
		t.Errorf("partial merge = %+v", merged)
	}
}

// TestTemplateLeavesOutWhatSparkplugCannotCarry — a struct with an array of
// structs keeps its other members: the unpublishable member is absent from
// the definition AND the instance, so the two still agree.
func TestTemplateLeavesOutWhatSparkplugCannotCarry(t *testing.T) {
	motor := motorDef()
	sd := &ir.StructDef{
		Name: "Line",
		Fields: []ir.StructField{
			{Name: "Speeds", Type: &ir.Type{Kind: ir.TypeArray, Elem: ir.RealT, ArrLen: 2}},
			{Name: "Motors", Type: &ir.Type{Kind: ir.TypeArray, Elem: &ir.Type{Kind: ir.TypeStruct, Struct: motor}, ArrLen: 2}},
			{Name: "Run", Type: ir.BoolT},
		},
		FieldIndex: map[string]int{"Speeds": 0, "Motors": 1, "Run": 2},
	}
	def := definitionTemplate(sd)
	inst, err := MetricFromValue("L1", ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: sd}), "Line")
	if err != nil {
		t.Fatal(err)
	}
	names := func(ms []Metric) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name+":"+m.Datatype.String())
		}
		return out
	}
	want := []string{"Speeds:DoubleArray", "Run:Boolean"}
	if got := names(def.Metrics); !reflect.DeepEqual(got, want) {
		t.Errorf("definition = %v, want %v", got, want)
	}
	if got := names(inst.Value.(*Template).Metrics); !reflect.DeepEqual(got, want) {
		t.Errorf("instance = %v, want %v", got, want)
	}
}

// RTU53's AOIs, in miniature: Get_TOD and Flow_Totalizer carry gettime :
// ARRAY [0..6] OF DINT; Levels is an ARRAY tag of its own; Lines is an array
// of structs, which Sparkplug cannot carry.
const arrTypesST = `
TYPE
	Get_TOD : STRUCT
		EnableIn : BOOL;
		gettime : ARRAY [0..6] OF DINT;
	END_STRUCT;
	Flow_Totalizer : STRUCT
		Total : REAL;
		gettime : ARRAY [0..6] OF DINT;
	END_STRUCT;
END_TYPE
`

const arrProgramST = `
PROGRAM Main
VAR_EXTERNAL
	RTC : Get_TOD;
	FT1 : Flow_Totalizer;
	Levels : ARRAY [0..2] OF REAL;
	Flags : ARRAY [0..9] OF BOOL;
	Lines : ARRAY [0..1] OF Get_TOD;
END_VAR
END_PROGRAM
`

func newArrayNode(t *testing.T, buf *bytes.Buffer, opts ...Option) (*Node, *fakeClient) {
	t.Helper()
	list := func(n int, z any) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = z
		}
		return out
	}
	rt, err := runtime.New(runtime.Options{
		Program:   arrProgramST,
		Libraries: []string{arrTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Typed("RTC", runtime.RoleState, "Get_TOD"),
			runtime.Typed("FT1", runtime.RoleState, "Flow_Totalizer"),
			runtime.State("Levels", list(3, 0.0)),
			runtime.State("Flags", list(10, false)),
			runtime.State("Lines", nil),
		},
	})
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	log := slogTo(buf)
	n, err := New(rt, Config{GroupID: "g", EdgeNode: "e", Log: log}, append([]Option{WithWritable("*")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	return n, fc
}

// TestBirthAndDataCarryArrays — the failure this whole change answers: an
// array member used to fail the entire NBIRTH ("unpublishable kind ARRAY").
// Now the birth carries the ARRAY tags, the array members of both templates
// in definition and instance, and leaves out (once logged) only the array
// of structs; an element change publishes the array in NDATA.
func TestBirthAndDataCarryArrays(t *testing.T) {
	var buf bytes.Buffer
	n, fc := newArrayNode(t, &buf)
	if err := n.birth(); err != nil {
		t.Fatalf("birth: %v", err)
	}
	births := fc.published("NBIRTH")
	if len(births) != 1 {
		t.Fatalf("%d NBIRTH, want 1", len(births))
	}
	p, err := DecodePayload(births[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Metric{}
	for _, m := range p.Metrics {
		byName[m.Name] = m
	}
	if m := byName["Levels"]; m.Datatype != spb.DataType_DoubleArray || !reflect.DeepEqual(m.Value, []float64{0, 0, 0}) {
		t.Errorf("Levels = %v %#v", m.Datatype, m.Value)
	}
	if m := byName["Flags"]; m.Datatype != spb.DataType_BooleanArray || len(m.Value.([]bool)) != 10 {
		t.Errorf("Flags = %v %#v", m.Datatype, m.Value)
	}
	if _, ok := byName["Lines"]; ok {
		t.Error("Lines (an array of structs) is in the birth")
	}
	for _, typ := range []string{"Get_TOD", "Flow_Totalizer"} {
		def, ok := byName[typ].Value.(*Template)
		if !ok || !def.IsDefinition {
			t.Fatalf("%s definition missing: %+v", typ, byName[typ])
		}
		var found bool
		for _, mm := range def.Metrics {
			if strings.EqualFold(mm.Name, "gettime") {
				found = mm.Datatype == spb.DataType_Int64Array && mm.IsNull
			}
		}
		if !found {
			t.Errorf("%s definition has no null Int64Array gettime: %+v", typ, def.Metrics)
		}
	}
	for _, tag := range []string{"RTC", "FT1"} {
		inst, ok := byName[tag].Value.(*Template)
		if !ok {
			t.Fatalf("%s instance missing", tag)
		}
		var found bool
		for _, mm := range inst.Metrics {
			if strings.EqualFold(mm.Name, "gettime") {
				found = mm.Datatype == spb.DataType_Int64Array && reflect.DeepEqual(mm.Value, make([]int64, 7))
			}
		}
		if !found {
			t.Errorf("%s instance has no 7-element gettime: %+v", tag, inst.Metrics)
		}
	}
	if c := strings.Count(buf.String(), "left out of the birth"); c != 1 {
		t.Errorf("unpublishable tag logged %d times, want 1:\n%s", c, buf.String())
	}

	// One element moves → NDATA carries the whole array.
	cur := n.rt.Tags().Snapshot()["RTC"]
	next := ir.CopyValue(cur)
	gi := next.Struct.FieldIndex["gettime"]
	next.Fld[gi].Arr[5] = ir.IntVal(42)
	n.rt.Tags().Set("RTC", next)
	tickWithin(t, n, 2*time.Second)
	data := fc.published("NDATA")
	if len(data) != 1 {
		t.Fatalf("%d NDATA after an element change, want 1", len(data))
	}
	d, err := DecodePayload(data[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Metrics) != 1 || d.Metrics[0].Name != "RTC" {
		t.Fatalf("NDATA metrics %+v, want RTC only", d.Metrics)
	}
	for _, mm := range d.Metrics[0].Value.(*Template).Metrics {
		if mm.Name == "gettime" && !reflect.DeepEqual(mm.Value, []int64{0, 0, 0, 0, 0, 42, 0}) {
			t.Errorf("NDATA gettime = %#v", mm.Value)
		}
	}

	// The unpublishable tag does not ask for a rebirth, tick after tick.
	for i := 0; i < 3; i++ {
		n.scanAndPublish()
	}
	if got := len(fc.published("NBIRTH")); got != 1 {
		t.Errorf("%d NBIRTHs, want 1 — the left-out tag is storming rebirths", got)
	}
}

// TestArrayCommandsRefused — array writes are not supported on the edge: a
// whole-array command and an array member of a template command are each
// refused (counted, logged), and the template's other members still apply.
func TestArrayCommandsRefused(t *testing.T) {
	var buf bytes.Buffer
	n, _ := newArrayNode(t, &buf)
	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "Levels", Datatype: spb.DataType_DoubleArray, Value: []float64{1, 2, 3}},
		{Name: "RTC", Datatype: spb.DataType_Template, Value: &Template{TemplateRef: "Get_TOD", Metrics: []Metric{
			{Name: "EnableIn", Datatype: spb.DataType_Boolean, Value: true},
			{Name: "gettime", Datatype: spb.DataType_Int64Array, Value: []int64{1, 2, 3, 4, 5, 6, 7}},
		}}},
	}})
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}
	for _, want := range []string{"tag=Levels", "tag=RTC.gettime", "array writes are not supported"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	snap := n.rt.Tags().Snapshot()
	if lv := snap["Levels"]; lv.Arr[0].F != 0 {
		t.Errorf("Levels written: %+v", lv)
	}
	rtc := snap["RTC"]
	if !rtc.Fld[rtc.Struct.FieldIndex["EnableIn"]].B {
		t.Error("RTC.EnableIn (a scalar member beside the refused array) was not applied")
	}
	if g := rtc.Fld[rtc.Struct.FieldIndex["gettime"]]; g.Arr[0].I != 0 {
		t.Errorf("RTC.gettime written: %+v", g)
	}
}

// An element of an array, named by index, is refused like a whole-array
// command: the node never births an indexed metric, and a pattern naming
// the array (which opens its members since the shared matcher walks "[n]")
// must not turn into element writes over Sparkplug (#506 review).
func TestArrayElementCommandRefused(t *testing.T) {
	var buf bytes.Buffer
	n, _ := newArrayNode(t, &buf)
	n.writable = []string{"Levels"}
	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "Levels[1]", Datatype: spb.DataType_Double, Value: 99.0},
	}})
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1", got)
	}
	if !strings.Contains(buf.String(), "array writes are not supported") || !strings.Contains(buf.String(), "tag=Levels[1]") {
		t.Errorf("log lacks the array refusal for Levels[1]:\n%s", buf.String())
	}
	if lv := n.rt.Tags().Snapshot()["Levels"]; lv.Arr[1].F == 99 {
		t.Errorf("Levels[1] written: %+v", lv)
	}
}

func TestRootTagCutsAtMemberAndElement(t *testing.T) {
	for in, want := range map[string]string{"Motor1.START": "Motor1", "Levels[1]": "Levels", "Drives[0].Speed": "Drives", "Plain": "Plain"} {
		if got := rootTag(in); got != want {
			t.Errorf("rootTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func slogTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
