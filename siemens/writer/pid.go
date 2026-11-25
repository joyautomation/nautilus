package writer

import (
	_ "embed"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// nautilus's PID (#1301 D2).
//
// TIA has no instruction that behaves like nautilus's PID (PID_Compact
// tunes itself, keeps its own modes and scales; CONT_C has other pins and
// another anti-windup), so the writer writes nautilus's own as an SCL
// FUNCTION_BLOCK "<prefix>PID": pid.scl, a line-by-line twin of
// lang/ir/builtins_pid.go. Each instance is a multi-instance in the block
// that declares it, as a project block's is (pous.go), and its calls,
// outputs and symbols go the same way; only its body is this fixed text
// instead of a translation.
//
// The pins are nautilus's names, as LREAL where nautilus's are REAL: a
// nautilus REAL holds 64 bits, so the PID's pins and working values do
// too, and an LREAL tag passes in and out exact. (A REAL tag that reads CV
// narrows to 32 bits there, as any LREAL stored into a REAL does; TIA
// warns.) DT and DB are SCL keywords, so they are written quoted ("DT"),
// as any such name is (sclName). dt, when DT is 0, is RUNTIME on a MEM of
// the instance's own: the seconds since this instance's last call, as
// nautilus measures it from the host clock per instance, so an instance
// called only on some scans gets the time since its own last call.
// RUNTIME's first return is not used (nautilus's first call has dt 0);
// the instance's _called flag says which call is the first.
//
// A PID is never kept across a restart: nautilus starts every clock block
// over (runtime/retainlocals.go), and TIA keeps a multi-instance's data
// with the section of the block that holds it (an FB's source has no
// NON_RETAIN section of its own: measured on V21, "The specified value
// "NON_RETAIN" is invalid"). So a PID a program declares in VAR RETAIN is
// written in the program's plain VAR section (declare), and the writer
// refuses any other block's instance in a RETAIN section (#1305), which
// keeps a PID nested in one non-retained too.
//
// Proof (2026-11-25, TIA V21 and the bench S7-1500, FW 3.1): compiled
// 0/0, and a trace compare on the CPU (docs/design/siemens-ide.md, "PID"):
// with DT given, every output of 400 scans equal to nautilus's to the
// last bit. A change to builtins_pid.go fails TestPIDTwinMatchesNautilus
// until pid.scl and that proof are redone.

//go:embed pid.scl
var pidSCL string

// pidName is the name nautilus's PID goes by, as a block (lower-cased key
// of library.pous).
const pidName = "PID"

// pidPOU is nautilus's PID as the writer holds a project block: its pins
// from nautilus's own definition (REAL as LREAL), its body pid.scl.
func pidPOU() *pou {
	def := ir.FBs[pidName]
	block := func(kind string, slots []ir.FBSlot) st.VarBlock {
		vb := st.VarBlock{Kind: kind}
		for _, s := range slots {
			vb.Variables = append(vb.Variables, st.VarDecl{Name: s.Name, Datatype: pidSlotType(s.Type)})
		}
		return vb
	}
	return &pou{kind: kindFB, name: pidName, builtin: true,
		vbs: []st.VarBlock{block("VAR_INPUT", def.Inputs), block("VAR_OUTPUT", def.Outputs)}}
}

func pidSlotType(t *ir.Type) string {
	if t.Kind == ir.TypeBool {
		return "BOOL"
	}
	return "LREAL"
}

// pidFile is the written block, named with the writer's prefix.
func (w *writer) pidFile(p *pou, s site) *pouFile {
	name := w.prefix + p.name
	data := "FUNCTION_BLOCK " + quote(name) + "\n" + pidSCL
	// No body lines: a compile message in it points at the first use (it
	// has no nautilus source line).
	return &pouFile{file: File{Path: name + ".scl", Data: []byte(data)}, src: Source{Path: s.path}}
}

// isPID says whether p is nautilus's PID (not a project block of that
// name).
func isPID(p *pou) bool { return p != nil && p.builtin && p.name == pidName }
