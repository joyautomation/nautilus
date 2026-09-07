package st

import (
	"math"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// pidHarness wires one PID instance to VAR_GLOBAL tags for every pin, so a
// Go test loop can drive inputs and read outputs across repeated ir.Run
// calls exactly like the TON tests above (compile once, mutate host.vals
// and host.now between scans).
const pidSrc = `
PROGRAM p
VAR_GLOBAL
    auto     : BOOL;
    pv       : REAL;
    sp       : REAL;
    kp       : REAL;
    ki       : REAL;
    kd       : REAL;
    cvMan    : REAL;
    cvMin    : REAL;
    cvMax    : REAL;
    direct   : BOOL;
    dt       : REAL;
    db       : REAL;
    reset    : BOOL;
    cvOut    : REAL;
    errOut   : REAL;
    satHiOut : BOOL;
    satLoOut : BOOL;
    pTermOut : REAL;
    iTermOut : REAL;
    dTermOut : REAL;
END_VAR
VAR
    pid1 : PID;
END_VAR
pid1(AUTO := auto, PV := pv, SP := sp, KP := kp, KI := ki, KD := kd,
     CV_MAN := cvMan, CV_MIN := cvMin, CV_MAX := cvMax, DIRECT := direct,
     DT := dt, DB := db, RESET := reset);
cvOut    := pid1.CV;
errOut   := pid1.ERR;
satHiOut := pid1.SAT_HI;
satLoOut := pid1.SAT_LO;
pTermOut := pid1.P_TERM;
iTermOut := pid1.I_TERM;
dTermOut := pid1.D_TERM;
END_PROGRAM`

// pidSrcNoDT is the same wiring but never binds DT — exercising the "0 =
// use the scan dt measured from the host clock" fallback.
const pidSrcNoDT = `
PROGRAM p
VAR_GLOBAL
    auto     : BOOL;
    pv       : REAL;
    sp       : REAL;
    kp       : REAL;
    ki       : REAL;
    kd       : REAL;
    cvMan    : REAL;
    cvMin    : REAL;
    cvMax    : REAL;
    direct   : BOOL;
    cvOut    : REAL;
    errOut   : REAL;
    iTermOut : REAL;
END_VAR
VAR
    pid1 : PID;
END_VAR
pid1(AUTO := auto, PV := pv, SP := sp, KP := kp, KI := ki, KD := kd,
     CV_MAN := cvMan, CV_MIN := cvMin, CV_MAX := cvMax, DIRECT := direct);
cvOut    := pid1.CV;
errOut   := pid1.ERR;
iTermOut := pid1.I_TERM;
END_PROGRAM`

// pidSrcDefaultRange never binds CV_MIN/CV_MAX at all, exercising the
// IEC-0..100 fallback for an unconfigured clamp range.
const pidSrcDefaultRange = `
PROGRAM p
VAR_GLOBAL
    auto  : BOOL;
    pv    : REAL;
    sp    : REAL;
    kp    : REAL;
    ki    : REAL;
    cvOut : REAL;
END_VAR
VAR
    pid1 : PID;
END_VAR
pid1(AUTO := auto, PV := pv, SP := sp, KP := kp, KI := ki);
cvOut := pid1.CV;
END_PROGRAM`

func compilePID(t *testing.T, src string) (*ir.Program, *ir.Frame) {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	return irProg, ir.NewFrame(irProg)
}

// runPID drives one scan and returns cvOut.
func runPID(t *testing.T, irProg *ir.Program, frame *ir.Frame, host *fakeHost) float64 {
	t.Helper()
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	return host.vals["cvOut"].F
}

// TestPIDStepResponseConverges drives a first-order plant (PV' = (K·CV −
// PV)/Tau) in Go, closes the loop through the PID block, and checks the
// process value settles on the setpoint step and stays there.
func TestPIDStepResponseConverges(t *testing.T) {
	host := newFakeHost()
	irProg, frame := compilePID(t, pidSrc)

	const (
		plantK   = 1.0  // steady-state gain: CV=100 -> PV settles at 100
		plantTau = 10.0 // seconds
		dtS      = 0.5  // scan interval, seconds
	)
	host.vals["auto"] = ir.BoolVal(true)
	host.vals["direct"] = ir.BoolVal(false) // reverse acting: error = SP-PV
	host.vals["kp"] = ir.RealVal(2.0)
	host.vals["ki"] = ir.RealVal(0.3) // repeats/sec
	host.vals["kd"] = ir.RealVal(0.0)
	host.vals["cvMin"] = ir.RealVal(0.0)
	host.vals["cvMax"] = ir.RealVal(100.0)
	host.vals["dt"] = ir.RealVal(dtS)
	host.vals["sp"] = ir.RealVal(50.0)
	host.vals["pv"] = ir.RealVal(0.0)

	pv := 0.0
	nowMs := int64(0)
	var cv float64
	for i := 0; i < 400; i++ { // 200s of virtual time
		nowMs += int64(dtS * 1000)
		host.now = nowMs
		host.vals["pv"] = ir.RealVal(pv)
		cv = runPID(t, irProg, frame, host)
		pv += (plantK*cv - pv) / plantTau * dtS
	}

	if math.Abs(pv-50.0) > 0.5 {
		t.Fatalf("PV after 200s = %.3f, want within 0.5 of SP=50", pv)
	}

	// Hold for another 50s and confirm it stays settled (no limit cycle,
	// no residual offset from an under-integrated loop).
	for i := 0; i < 100; i++ {
		nowMs += int64(dtS * 1000)
		host.now = nowMs
		host.vals["pv"] = ir.RealVal(pv)
		cv = runPID(t, irProg, frame, host)
		pv += (plantK*cv - pv) / plantTau * dtS
		if math.Abs(pv-50.0) > 0.6 {
			t.Fatalf("PV drifted off setpoint while holding: %.3f at step %d", pv, i)
		}
	}
}

// TestPIDAntiWindupStaysClamped pins PV far below SP forever (so the loop
// saturates high) and checks: CV never exceeds CV_MAX, SAT_HI is asserted,
// and the integral term plateaus instead of growing without bound — then,
// once the error reverses, CV comes off the rail immediately rather than
// staying pinned by a wound-up integral.
func TestPIDAntiWindupStaysClamped(t *testing.T) {
	host := newFakeHost()
	irProg, frame := compilePID(t, pidSrc)

	host.vals["auto"] = ir.BoolVal(true)
	host.vals["direct"] = ir.BoolVal(false)
	host.vals["kp"] = ir.RealVal(1.0)
	host.vals["ki"] = ir.RealVal(0.5)
	host.vals["kd"] = ir.RealVal(0.0)
	host.vals["cvMin"] = ir.RealVal(0.0)
	host.vals["cvMax"] = ir.RealVal(100.0)
	host.vals["dt"] = ir.RealVal(1.0)
	host.vals["sp"] = ir.RealVal(1000.0) // unreachable setpoint -> permanent saturation
	host.vals["pv"] = ir.RealVal(0.0)

	nowMs := int64(0)
	var lastITerm float64
	for i := 0; i < 60; i++ {
		nowMs += 1000
		host.now = nowMs
		runPID(t, irProg, frame, host)
		cv := host.vals["cvOut"].F
		if cv > 100.0+1e-9 {
			t.Fatalf("scan %d: CV=%.6f exceeds CV_MAX=100", i, cv)
		}
		if !host.vals["satHiOut"].B {
			t.Fatalf("scan %d: expected SAT_HI once saturated", i)
		}
		lastITerm = host.vals["iTermOut"].F
	}
	// I_TERM must have plateaued, not run away, once the output is clamped.
	nowMs += 1000
	host.now = nowMs
	runPID(t, irProg, frame, host)
	if got := host.vals["iTermOut"].F; math.Abs(got-lastITerm) > 1e-9 {
		t.Fatalf("I_TERM still growing after saturation: was %.6f, now %.6f", lastITerm, got)
	}

	// Error reverses hard: CV must come off the high rail immediately,
	// which only happens if the integral wasn't allowed to wind up.
	host.vals["sp"] = ir.RealVal(0.0)
	host.vals["pv"] = ir.RealVal(1000.0)
	nowMs += 1000
	host.now = nowMs
	cv := runPID(t, irProg, frame, host)
	if cv > 5.0 {
		t.Fatalf("CV=%.3f after hard error reversal, want it to drop immediately (anti-windup)", cv)
	}
}

// TestPIDManualToAutoBumpless checks that switching AUTO on after running
// in MANUAL produces no CV jump: the integral is continuously back-solved
// while in MANUAL so P+I+D already equals CV_MAN the instant AUTO flips.
func TestPIDManualToAutoBumpless(t *testing.T) {
	host := newFakeHost()
	irProg, frame := compilePID(t, pidSrc)

	host.vals["auto"] = ir.BoolVal(false)
	host.vals["direct"] = ir.BoolVal(false)
	host.vals["kp"] = ir.RealVal(2.0)
	host.vals["ki"] = ir.RealVal(0.4)
	host.vals["kd"] = ir.RealVal(0.0)
	host.vals["cvMin"] = ir.RealVal(0.0)
	host.vals["cvMax"] = ir.RealVal(100.0)
	host.vals["dt"] = ir.RealVal(1.0)
	host.vals["sp"] = ir.RealVal(50.0)
	host.vals["pv"] = ir.RealVal(30.0) // error stays nonzero throughout
	host.vals["cvMan"] = ir.RealVal(42.0)

	nowMs := int64(0)
	var cvBefore float64
	for i := 0; i < 10; i++ {
		nowMs += 1000
		host.now = nowMs
		cvBefore = runPID(t, irProg, frame, host)
	}
	if math.Abs(cvBefore-42.0) > 1e-9 {
		t.Fatalf("CV in MANUAL = %.6f, want CV_MAN = 42", cvBefore)
	}

	host.vals["auto"] = ir.BoolVal(true)
	nowMs += 1000
	host.now = nowMs
	cvAfter := runPID(t, irProg, frame, host)

	if math.Abs(cvAfter-cvBefore) > 1e-6 {
		t.Fatalf("CV jumped on MANUAL->AUTO: before=%.6f after=%.6f", cvBefore, cvAfter)
	}
}

// TestPIDDirectVsReverse checks the DIRECT pin's acting-direction
// convention: reverse (FALSE) drives CV up when PV is below SP (a heater);
// direct (TRUE) drives CV up when PV is above SP (a cooling valve).
func TestPIDDirectVsReverse(t *testing.T) {
	run := func(direct bool, pv, sp float64) float64 {
		host := newFakeHost()
		irProg, frame := compilePID(t, pidSrc)
		host.vals["auto"] = ir.BoolVal(true)
		host.vals["direct"] = ir.BoolVal(direct)
		host.vals["kp"] = ir.RealVal(1.0)
		host.vals["ki"] = ir.RealVal(0.0)
		host.vals["kd"] = ir.RealVal(0.0)
		host.vals["cvMin"] = ir.RealVal(-100.0)
		host.vals["cvMax"] = ir.RealVal(100.0)
		host.vals["dt"] = ir.RealVal(1.0)
		host.vals["sp"] = ir.RealVal(sp)
		host.vals["pv"] = ir.RealVal(pv)
		host.now = 1000
		return runPID(t, irProg, frame, host)
	}

	// PV below SP: reverse acting should call for more CV (heat), direct
	// acting should call for less.
	if cv := run(false, 40.0, 50.0); cv != 10.0 {
		t.Errorf("reverse acting, PV<SP: CV=%.3f, want 10 (SP-PV)", cv)
	}
	if cv := run(true, 40.0, 50.0); cv != -10.0 {
		t.Errorf("direct acting, PV<SP: CV=%.3f, want -10 (PV-SP)", cv)
	}
	// PV above SP: signs flip.
	if cv := run(false, 60.0, 50.0); cv != -10.0 {
		t.Errorf("reverse acting, PV>SP: CV=%.3f, want -10 (SP-PV)", cv)
	}
	if cv := run(true, 60.0, 50.0); cv != 10.0 {
		t.Errorf("direct acting, PV>SP: CV=%.3f, want 10 (PV-SP)", cv)
	}
}

// TestPIDDTZeroUsesScanDt leaves DT unbound (0) and checks the block
// measures its own scan interval from the host clock instead — the same
// virtual-time source TON uses for PT/ET, so it stays deterministic under
// the acceptance harness.
func TestPIDDTZeroUsesScanDt(t *testing.T) {
	host := newFakeHost()
	irProg, frame := compilePID(t, pidSrcNoDT)

	host.vals["auto"] = ir.BoolVal(true)
	host.vals["direct"] = ir.BoolVal(false)
	host.vals["kp"] = ir.RealVal(1.0)
	host.vals["ki"] = ir.RealVal(1.0)
	host.vals["kd"] = ir.RealVal(0.0)
	host.vals["cvMin"] = ir.RealVal(-1e6)
	host.vals["cvMax"] = ir.RealVal(1e6)
	host.vals["sp"] = ir.RealVal(10.0)
	host.vals["pv"] = ir.RealVal(0.0) // constant PV -> error = 10 every scan

	nowMs := int64(0)
	const scans = 6
	for i := 0; i < scans; i++ {
		nowMs += 1000 // 1s per scan, virtual
		host.now = nowMs
		runPID(t, irProg, frame, host)
	}
	// First call measures dt=0 (no previous timestamp), so only (scans-1)
	// seconds of integration have happened: I_TERM = KP*KI*error*elapsed.
	want := 10.0 * float64(scans-1)
	got := host.vals["iTermOut"].F
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("I_TERM with DT=0 fallback = %.6f, want %.6f (host-clock-measured dt)", got, want)
	}
}

// TestPIDDefaultClampRange checks that leaving both CV_MIN and CV_MAX
// unbound (both at their IEC zero default) falls back to the IEC 0..100
// range instead of clamping every output to zero.
func TestPIDDefaultClampRange(t *testing.T) {
	host := newFakeHost()
	irProg, frame := compilePID(t, pidSrcDefaultRange)

	host.vals["auto"] = ir.BoolVal(true)
	host.vals["kp"] = ir.RealVal(1.0)
	host.vals["ki"] = ir.RealVal(0.0)
	host.vals["sp"] = ir.RealVal(1000.0)
	host.vals["pv"] = ir.RealVal(0.0)
	host.now = 1000

	cv := runPID(t, irProg, frame, host)
	if cv != 100.0 {
		t.Fatalf("CV with unbound CV_MIN/CV_MAX = %.3f, want 100 (IEC default range, not 0)", cv)
	}
}
