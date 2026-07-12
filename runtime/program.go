package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
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

// Language reports a program source's language: "fbd" when it carries an
// FBD netlist block, else "st". The runtime accepts both everywhere a
// program is given (boot, online edit); FBD transpiles through lang/fbd on
// the way in, and the ORIGINAL source is what Source/Hash/Dirty describe —
// so a workspace .fbd file compares 1:1 with what the controller reports.
func Language(src string) string {
	if fbdBlockRe.MatchString(src) {
		return "fbd"
	}
	return "st"
}

// lowerSource compiles original program source — ST, or ST with an FBD
// program body — down to the IR.
func lowerSource(src string) (*ir.Program, error) {
	if Language(src) == "fbd" {
		stSrc, err := fbd.Transpile(src)
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
