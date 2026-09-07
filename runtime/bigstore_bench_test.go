package runtime_test

import (
	"fmt"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// The shape a real site has, not the shape a micro-benchmark likes: a
// 1s-scan controller whose tag store is dominated by UDT instances a
// Sparkplug host driver delivers — 500 struct tags of 40 members each plus
// 60 loose scalars, 560 driver-bound inputs, and a program that reads ten of
// them. Per-scan cost here is almost entirely RUNTIME work (driver map copy,
// tag-store writes, observers), not logic: that is exactly the number this
// file exists to hold down.

const (
	benchStructTags   = 500
	benchStructFields = 40
	benchScalarTags   = 60
	benchProgReads    = 10
)

// bigLib declares the 40-member UDT the store is full of.
func bigLib() string {
	var b strings.Builder
	b.WriteString("TYPE\n  Blk : STRUCT\n")
	for i := 0; i < benchStructFields; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&b, "    F%d : REAL;\n", i)
		case 1:
			fmt.Fprintf(&b, "    F%d : BOOL;\n", i)
		case 2:
			fmt.Fprintf(&b, "    F%d : INT;\n", i)
		default:
			fmt.Fprintf(&b, "    F%d : REAL;\n", i)
		}
	}
	b.WriteString("  END_STRUCT;\nEND_TYPE\n")
	return b.String()
}

// bigProg reads benchProgReads struct members and writes one output — a
// deliberately trivial scan body, so the benchmark measures the runtime
// around the logic rather than the logic.
func bigProg() string {
	var b strings.Builder
	b.WriteString("PROGRAM Main\nVAR_EXTERNAL\n")
	for i := 0; i < benchProgReads; i++ {
		fmt.Fprintf(&b, "    S%d : Blk;\n", i)
	}
	b.WriteString("    Acc : REAL;\nEND_VAR\n")
	b.WriteString("Acc := 0.0;\n")
	for i := 0; i < benchProgReads; i++ {
		fmt.Fprintf(&b, "Acc := Acc + S%d.F0;\n", i)
	}
	b.WriteString("END_PROGRAM\n")
	return b.String()
}

// blkDef is the StructDef a typed driver attaches to the values it delivers
// — the same shape the ST TYPE compiles to, built by hand so the benchmark's
// driver can hand whole UDT values across the io seam like a real one.
func blkDef() *ir.StructDef {
	sd := &ir.StructDef{Name: "Blk", FieldIndex: map[string]int{}}
	for i := 0; i < benchStructFields; i++ {
		var t *ir.Type
		switch i % 4 {
		case 0, 3:
			t = &ir.Type{Kind: ir.TypeReal}
		case 1:
			t = &ir.Type{Kind: ir.TypeBool}
		case 2:
			t = &ir.Type{Kind: ir.TypeInt}
		}
		name := fmt.Sprintf("F%d", i)
		sd.FieldIndex[name] = i
		sd.Fields = append(sd.Fields, ir.StructField{Name: name, Type: t})
	}
	return sd
}

func blkValue(sd *ir.StructDef, seed float64) ir.Value {
	v := ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: make([]ir.Value, benchStructFields)}
	for i := range v.Fld {
		switch i % 4 {
		case 0, 3:
			v.Fld[i] = ir.RealVal(seed + float64(i))
		case 1:
			v.Fld[i] = ir.BoolVal(i%8 == 1)
		case 2:
			v.Fld[i] = ir.IntVal(int64(i))
		}
	}
	return v
}

func bigTagNames() (structs, scalars []string) {
	for i := 0; i < benchStructTags; i++ {
		structs = append(structs, fmt.Sprintf("S%d", i))
	}
	for i := 0; i < benchScalarTags; i++ {
		scalars = append(scalars, fmt.Sprintf("N%d", i))
	}
	return
}

// bigRuntime builds the 560-input controller over the Memory driver, already
// primed with one delivery of every input.
func bigRuntime(tb testing.TB) *runtime.Runtime {
	tb.Helper()
	sd := blkDef()
	structs, scalars := bigTagNames()

	drv := nio.NewMemory()
	seed := nio.Values{}
	for i, n := range structs {
		seed[n] = blkValue(sd, float64(i))
	}
	for i, n := range scalars {
		seed[n] = float64(i)
	}
	if err := drv.WriteOutputs(seed); err != nil {
		tb.Fatal(err)
	}

	defs := []runtime.TagDef{runtime.State("Acc", 0.0)}
	for _, n := range structs {
		defs = append(defs, runtime.Typed(n, runtime.RoleInput, "Blk"))
	}
	for _, n := range scalars {
		defs = append(defs, runtime.Input(n))
	}
	rt, err := runtime.New(runtime.Options{
		Program:   bigProg(),
		Libraries: []string{bigLib()},
		Driver:    drv,
		Tags:      defs,
		Outputs:   []string{"Acc"},
	})
	if err != nil {
		tb.Fatalf("compile: %v", err)
	}
	rt.Scan() // warm: first delivery of every input, one-time costs
	return rt
}

// BenchmarkScanBigStore is the headline number: one full scan of the
// 560-input controller with a ten-line program. Everything it measures
// beyond a few microseconds of logic is runtime overhead.
func BenchmarkScanBigStore(b *testing.B) {
	rt := bigRuntime(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt.Scan()
	}
}

// BenchmarkScanBigStoreChanging is the same scan with 5% of the inputs
// genuinely moving each cycle — the case where the change-detection fast
// paths must still do the real work.
func BenchmarkScanBigStoreChanging(b *testing.B) {
	sd := blkDef()
	structs, scalars := bigTagNames()
	drv := nio.NewMemory()
	seed := nio.Values{}
	for i, n := range structs {
		seed[n] = blkValue(sd, float64(i))
	}
	for i, n := range scalars {
		seed[n] = float64(i)
	}
	if err := drv.WriteOutputs(seed); err != nil {
		b.Fatal(err)
	}
	defs := []runtime.TagDef{runtime.State("Acc", 0.0)}
	for _, n := range structs {
		defs = append(defs, runtime.Typed(n, runtime.RoleInput, "Blk"))
	}
	for _, n := range scalars {
		defs = append(defs, runtime.Input(n))
	}
	rt, err := runtime.New(runtime.Options{
		Program:   bigProg(),
		Libraries: []string{bigLib()},
		Driver:    drv,
		Tags:      defs,
		Outputs:   []string{"Acc"},
	})
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	rt.Scan()

	churn := benchStructTags / 20 // 5%
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		upd := nio.Values{}
		for k := 0; k < churn; k++ {
			n := structs[(i*churn+k)%len(structs)]
			upd[n] = blkValue(sd, float64(i+k))
		}
		_ = drv.WriteOutputs(upd)
		b.StartTimer()
		rt.Scan()
	}
}

// BenchmarkTagsSnapshot isolates the whole-store copy every observer,
// Sparkplug publish tick and SSE frame used to pay for.
func BenchmarkTagsSnapshot(b *testing.B) {
	rt := bigRuntime(b)
	tags := rt.Tags()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tags.Snapshot()
	}
}
