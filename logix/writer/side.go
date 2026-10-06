package writer

import "strings"

// Side code: logic nautilus adds beside the user's program for its own
// purposes — testing, verification, metrics — in a Logix program of its
// own (SideProgram), scheduled after the user's program in the same task.
// The user's routine is never touched: what Studio 5000 shows in
// MainRoutine is the user's source and nothing else, and the side program
// is plainly labelled as nautilus's.
//
// The first piece is the heartbeat: a controller DINT the side program
// increments once per task scan. A live test (naut test --target logix)
// waits on it to spend exactly `scans: n`, where a wall clock could only
// approximate; live values get a liveness signal for free.

func (lw *lowered) side() {
	hb := strings.TrimSpace(lw.opts.Side.Heartbeat)
	if hb == "" {
		return
	}
	if !lw.checkName(hb, 0, "") {
		return
	}
	if _, clash := lw.vars[strings.ToLower(hb)]; clash {
		lw.diag(ruleName, 0, "", "side code: heartbeat tag %s clashes with a declared variable; pick another name (target.logix.side.heartbeat)", hb)
		return
	}
	lw.ctrlTags = append(lw.ctrlTags, tagDef{Name: hb, DataType: "DINT", Desc: "nautilus side code: task scans since download (wraps)"})
	lw.sideRungs = append(lw.sideRungs, rungOut{
		Comment: "nautilus side code: one count per task scan — a live test waits on this to spend exactly N scans",
		Text:    "ADD(" + hb + ",1," + hb + ")",
		Source:  "side:heartbeat",
	})
}
