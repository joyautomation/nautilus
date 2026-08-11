package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
)

// Program is a compiled IEC 61131-3 program plus its retained frame (the
// plain VAR state that persists between scans, like a PID integral).
// Recompiling swaps both atomically; a failed compile leaves the running
// program untouched. Safe for concurrent Run/Swap.
type Program struct {
	mu         sync.Mutex
	source     string
	prog       *ir.Program
	frame      *ir.Frame
	compiledAt time.Time
	scans      uint64
	lastErr    string

	// bootSource is what Compile was first given — the program the deployed
	// binary embeds. A running source that differs is an online edit in
	// progress (Dirty), ephemeral by design: restarts revert to boot.
	bootSource string

	// One-step undo for online edits: the previous program AND its frame,
	// kept by SwapWarm so Rollback is instant and stateful.
	prevSource string
	prevProg   *ir.Program
	prevFrame  *ir.Frame
}

// fbdBlockRe detects a Function Block Diagram netlist in program source: an
// FBD keyword alone on its line, the same line-based convention lang/fbd's
// splitter uses.
var fbdBlockRe = regexp.MustCompile(`(?mi)^\s*FBD\s*$`)

// Language reports a program source's language: "ld" for a Ladder Diagram
// rung block, "fbd" for an FBD netlist block, "sfc" for a Sequential
// Function Chart block, else "st". The runtime accepts all four everywhere
// a program is given (boot, online edit); graphical/chart languages
// transpile on the way in — LD to the FBD netlist, FBD to ST, SFC directly
// to ST (a sibling front-end, not a stage in the LD/FBD chain — see
// docs/design/sfc.md §3) — and the ORIGINAL source is what Source/Hash/
// Dirty describe, so a workspace .ld/.fbd/.sfc file compares 1:1 with what
// the controller reports.
func Language(src string) string {
	if ld.HasBlock(src) {
		return "ld"
	}
	if fbdBlockRe.MatchString(src) {
		return "fbd"
	}
	if sfc.HasBlock(src) {
		return "sfc"
	}
	return "st"
}

// lowerSource compiles original program source — ST, or ST with an FBD,
// LD, or SFC program body — down to the IR.
func lowerSource(src string) (*ir.Program, error) {
	if Language(src) == "ld" {
		fbdSrc, err := ld.Transpile(src)
		if err != nil {
			return nil, err
		}
		src = fbdSrc
	}
	if Language(src) == "fbd" {
		stSrc, err := fbd.Transpile(src)
		if err != nil {
			return nil, err
		}
		src = stSrc
	}
	if Language(src) == "sfc" {
		// SFC transpiles directly to ST — not implemented yet (see
		// lang/sfc/sfc.go); this errors cleanly until that lands.
		stSrc, err := sfc.Transpile(src)
		if err != nil {
			return nil, err
		}
		src = stSrc
	}
	ast, err := st.Parse(src)
	if err != nil {
		return nil, err
	}
	return st.Lower(ast)
}

// Compile parses and lowers program source (ST or FBD) into a runnable
// program.
func Compile(src string) (*Program, error) {
	p := &Program{bootSource: src}
	if err := p.Swap(src); err != nil {
		return nil, err
	}
	return p, nil
}

// Swap replaces the running program with newly-compiled source, resetting the
// retained frame. On a compile error the old program keeps running.
func (p *Program) Swap(src string) error {
	prog, err := lowerSource(src)
	if err == nil {
		p.mu.Lock()
		p.prog, p.frame = prog, ir.NewFrame(prog)
		p.source, p.compiledAt, p.scans, p.lastErr = src, time.Now(), 0, ""
		p.mu.Unlock()
		return nil
	}
	p.mu.Lock()
	p.lastErr = err.Error()
	p.mu.Unlock()
	return err
}

// POUOf extracts the `PROGRAM <Name>` POU name from IEC source, "" if none
// — how an incoming online edit names the program it targets. One
// definition for the whole toolchain (stproject.POU).
func POUOf(src string) string { return stproject.POU(src) }

// POU returns the program's IEC POU name (`PROGRAM <Name>`), "" if the
// source has none. Programs in a resource are addressed by this name for
// online edits — it's the identity that survives recomposition.
func (p *Program) POU() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return POUOf(p.source)
}

// Run executes one scan of the program against the tag store.
func (p *Program) Run(tags *Tags) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ir.Run(p.prog, p.frame, tags); err != nil {
		p.lastErr = err.Error()
		return err
	}
	p.scans++
	return nil
}

// SwapReport describes what a warm swap did.
type SwapReport struct {
	Hash string `json:"hash"`
	// Resets are variables that could not carry over: new names, renamed
	// names, or changed types. They restarted at their declared initial
	// values.
	Resets []string `json:"resets,omitempty"`
}

// SwapWarm replaces the running program like Swap, but migrates retained
// state by name and type — the online-edit transfer: a PID integral, timer,
// or counter survives the edit. The outgoing program and frame are kept for
// one Rollback. On a compile error the running program is untouched.
func (p *Program) SwapWarm(src string) (SwapReport, error) {
	prog, err := lowerSource(src)
	if err == nil {
		p.mu.Lock()
		frame, resets := ir.MigrateFrame(prog, p.prog, p.frame)
		p.prevSource, p.prevProg, p.prevFrame = p.source, p.prog, p.frame
		p.prog, p.frame = prog, frame
		p.source, p.compiledAt, p.scans, p.lastErr = src, time.Now(), 0, ""
		p.mu.Unlock()
		return SwapReport{Hash: sourceHash(src), Resets: resets}, nil
	}
	p.mu.Lock()
	p.lastErr = err.Error()
	p.mu.Unlock()
	return SwapReport{}, err
}

// Rollback restores the program and frame exactly as they were before the
// last SwapWarm — a one-step, stateful undo. It errors when there is
// nothing to roll back to.
func (p *Program) Rollback() (SwapReport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prevProg == nil {
		return SwapReport{}, errors.New("runtime: nothing to roll back")
	}
	p.source, p.prog, p.frame = p.prevSource, p.prevProg, p.prevFrame
	p.prevSource, p.prevProg, p.prevFrame = "", nil, nil
	p.compiledAt, p.scans, p.lastErr = time.Now(), 0, ""
	return SwapReport{Hash: sourceHash(p.source)}, nil
}

// Hash identifies the running source (first 12 hex chars of its SHA-256).
func (p *Program) Hash() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return sourceHash(p.source)
}

// Dirty reports whether the running source differs from what the binary
// booted with — an online edit is in progress.
func (p *Program) Dirty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.source != p.bootSource
}

// CanRollback reports whether a previous program is available.
func (p *Program) CanRollback() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prevProg != nil
}

func sourceHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:6])
}

// ResetFrame discards retained state — call on a redundancy takeover so the
// program warm-starts against live field values.
func (p *Program) ResetFrame() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frame = ir.NewFrame(p.prog)
}

// Locals returns the current values of the program's retained VAR slots.
func (p *Program) Locals() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{}
	for i, s := range p.prog.Slots {
		if s.Kind == ir.VarLocal && i < len(p.frame.Slots) {
			out[s.Name] = plain(p.frame.Slots[i])
		}
	}
	return out
}

// Globals reports every PLC variable the program binds, with the type it
// was declared as — the VAR_EXTERNAL block as the compiler resolved it,
// diagram languages included (they transpile to ST before lowering).
//
// This is introspection for tooling, not a scan path. It answers a question
// the tag store cannot until something has run: an output tag with no seed
// exists in no snapshot, yet the program declares its type and a test may
// well assert on it.
func (p *Program) Globals() map[string]*ir.Type {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prog == nil {
		return nil
	}
	out := make(map[string]*ir.Type, len(p.prog.Globals))
	for name, t := range p.prog.Globals {
		out[name] = t
	}
	return out
}

// Types reports every TYPE this program's sources declare, resolved. A
// manifest tag naming a UDT resolves against this.
func (p *Program) Types() map[string]*ir.Type {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prog == nil {
		return nil
	}
	out := make(map[string]*ir.Type, len(p.prog.Types))
	for name, t := range p.prog.Types {
		out[name] = t
	}
	return out
}

// GlobalUses reports which of this program's globals are read and which are
// written — the split `nautilus check` needs to tell a missing manifest entry
// that costs an HMI description from one that faults the scan.
func (p *Program) GlobalUses() ir.GlobalUse {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prog == nil {
		return ir.GlobalUse{Read: map[string]bool{}, Written: map[string]bool{}}
	}
	return p.prog.GlobalUses()
}

func (p *Program) Source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.source
}

// Status describes the program for an HMI.
type Status struct {
	CompiledAt int64  `json:"compiledAt"`
	Scans      uint64 `json:"scans"`
	Error      string `json:"error,omitempty"`
}

func (p *Program) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{CompiledAt: p.compiledAt.UnixMilli(), Scans: p.scans, Error: p.lastErr}
}
