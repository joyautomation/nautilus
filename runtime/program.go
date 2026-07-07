package runtime

import (
	"sync"
	"time"

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
}

// Compile parses and lowers ST source into a runnable program.
func Compile(src string) (*Program, error) {
	p := &Program{}
	if err := p.Swap(src); err != nil {
		return nil, err
	}
	return p, nil
}

// Swap replaces the running program with newly-compiled source, resetting the
// retained frame. On a compile error the old program keeps running.
func (p *Program) Swap(src string) error {
	ast, err := st.Parse(src)
	if err == nil {
		var prog *ir.Program
		if prog, err = st.Lower(ast); err == nil {
			p.mu.Lock()
			p.prog, p.frame = prog, ir.NewFrame(prog)
			p.source, p.compiledAt, p.scans, p.lastErr = src, time.Now(), 0, ""
			p.mu.Unlock()
			return nil
		}
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
