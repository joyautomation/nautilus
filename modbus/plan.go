// plan.go turns tag bindings into the block-read plan: which requests
// actually hit each device. A naive client issues one Modbus request per
// variable per interval (tens of requests a second across a skid); coalescing
// bindings into block reads per (source, table, scan class) turns a 16-channel
// analyser's 16 requests into one.
// Pure functions over the manifest — New validates and plans offline, so
// `naut check` and `build` pass with no device in sight, and
// `naut modbus import --plan` prints Plan.String for the commissioning
// tech.
package modbus

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultClass is the scan class of every binding that isn't assigned one.
// Its rate is WithScanRate.
const DefaultClass = "default"

// NoPoll is the reserved scan class for tags that stay in the catalog but
// are never polled. They remain valid write targets — a command register the
// program only ever writes belongs here.
const NoPoll = "none"

// DefaultBlockGap is how many unaddressed registers (or coils) a block may
// span to avoid a second round-trip: reading eight unused words is cheaper
// than another request. Gap 0 is the escape hatch for devices that fault a
// read touching an unimplemented register (brief §9 risk 4).
const DefaultBlockGap = 8

// Plan is the complete read schedule: every block request the driver will
// issue, in a deterministic order (source, table, class, address).
type Plan struct {
	Blocks []Block
}

// Block is one read request: Count registers/coils from Start on one
// source's table, polled with one scan class, decoding into Bindings.
type Block struct {
	Source string
	Table  string
	// Class is the resolved scan class (never empty — DefaultClass stands
	// in for unassigned bindings).
	Class string
	Start uint16
	Count uint16
	// Bindings are the tags this block serves, sorted by address. Offsets
	// are relative to Start via each binding's own Address.
	Bindings []TagBinding
}

// FC is the read function code for the block's table.
func (b Block) FC() byte {
	switch b.Table {
	case TableHolding:
		return FCReadHoldingRegisters
	case TableInput:
		return FCReadInputRegisters
	case TableCoil:
		return FCReadCoils
	default:
		return FCReadDiscreteInputs
	}
}

// BuildPlan validates the manifest and computes the block plan. gap is the
// coalescing tolerance in registers/coils; negative means DefaultBlockGap
// (so 0 — never bridge a hole — stays expressible). Blocks respect the
// protocol caps (125 registers / 2000 coils per read) and each source's
// MaxBlock override.
//
// Excluded from the plan: NoPoll bindings (catalog only) and WriteOnly
// bindings (registers that cannot be read back). A writable binding that is
// neither stays in the plan — the read-back is the source of truth after a
// restart.
func BuildPlan(m Manifest, gap int) (Plan, error) {
	if err := m.Validate(); err != nil {
		return Plan{}, err
	}
	if gap < 0 {
		gap = DefaultBlockGap
	}

	maxBlock := make(map[string]int, len(m.Sources))
	for _, s := range m.Sources {
		maxBlock[s.ID] = s.MaxBlock
	}

	// Group bindings by (source, table, class).
	type key struct{ source, table, class string }
	groups := map[key][]TagBinding{}
	for _, t := range m.Tags {
		class := t.ScanClass
		if class == "" {
			class = DefaultClass
		}
		if class == NoPoll || t.WriteOnly {
			continue
		}
		k := key{t.Source, t.Table, class}
		groups[k] = append(groups[k], t)
	}

	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.source != b.source {
			return a.source < b.source
		}
		if a.table != b.table {
			return tableRank(a.table) < tableRank(b.table)
		}
		return a.class < b.class
	})

	var plan Plan
	for _, k := range keys {
		bindings := groups[k]
		sort.Slice(bindings, func(i, j int) bool {
			if bindings[i].Address != bindings[j].Address {
				return bindings[i].Address < bindings[j].Address
			}
			return bindings[i].Name < bindings[j].Name
		})

		cap := maxReadRegisters
		if !registerTable(k.table) {
			cap = maxReadBits
		}
		if mb := maxBlock[k.source]; mb > 0 && mb < cap {
			cap = mb
		}

		var cur *Block
		var end int // one past the last covered address of cur
		for _, t := range bindings {
			f, _ := t.format() // Validate already vetted it
			a, w := int(t.Address), f.Words()
			if !registerTable(k.table) {
				w = 1 // one coil per binding
			}
			if w > cap {
				return Plan{}, fmt.Errorf("tag %s: format %s needs %d registers but source %s allows blocks of %d",
					t.Name, f, w, k.source, cap)
			}
			if cur != nil && a <= end+gap && max(end, a+w)-int(cur.Start) <= cap {
				end = max(end, a+w)
				cur.Count = uint16(end - int(cur.Start))
				cur.Bindings = append(cur.Bindings, t)
				continue
			}
			plan.Blocks = append(plan.Blocks, Block{
				Source: k.source, Table: k.table, Class: k.class,
				Start: t.Address, Count: uint16(w), Bindings: []TagBinding{t},
			})
			cur = &plan.Blocks[len(plan.Blocks)-1]
			end = a + w
		}
	}
	return plan, nil
}

// tableRank orders tables for deterministic output: registers before bits,
// matching how a datasheet reads.
func tableRank(table string) int {
	switch table {
	case TableHolding:
		return 0
	case TableInput:
		return 1
	case TableCoil:
		return 2
	default:
		return 3
	}
}

// String renders the plan for humans — the `naut modbus import --plan`
// output. One header per source, one line per block request, every tag with
// its address so a commissioning tech can match it against the datasheet:
//
//	source FTIR_I: 1 block
//	  FC3 holding 0..57 (58 registers, class default): FTIR_I_CO@14, FTIR_I_NO@16
func (p Plan) String() string {
	var b strings.Builder
	perSource := map[string][]Block{}
	var order []string
	for _, blk := range p.Blocks {
		if _, ok := perSource[blk.Source]; !ok {
			order = append(order, blk.Source)
		}
		perSource[blk.Source] = append(perSource[blk.Source], blk)
	}
	if len(order) == 0 {
		return "plan: no blocks (nothing polled)\n"
	}
	for _, src := range order {
		blocks := perSource[src]
		fmt.Fprintf(&b, "source %s: %d %s\n", src, len(blocks), plural(len(blocks), "block"))
		for _, blk := range blocks {
			unit := "registers"
			if !registerTable(blk.Table) {
				unit = "coils"
			}
			names := make([]string, len(blk.Bindings))
			for i, t := range blk.Bindings {
				names[i] = fmt.Sprintf("%s@%d", t.Name, t.Address)
			}
			fmt.Fprintf(&b, "  FC%d %s %d..%d (%d %s, class %s): %s\n",
				blk.FC(), blk.Table, blk.Start, int(blk.Start)+int(blk.Count)-1,
				blk.Count, unit, blk.Class, strings.Join(names, ", "))
		}
	}
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
