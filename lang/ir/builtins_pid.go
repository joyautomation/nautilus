package ir

import "math"

// ─── PID: closed-loop control ───────────────────────────────────────
//
// A positional (non-velocity) PID in the ISA "standard form":
//
//	CV = KP * ( error  +  KI * ∫error dt  +  KD * d(PV)/dt )
//
// KI is repeats/second (1/TI — zero disables integral action). KD is
// seconds (TD — zero disables derivative action). Because both ride on
// KP, KP = 0 disables the whole controller, not just the P term; that
// mirrors the standard form and is why KI/KD (not KP) are documented as
// the "0 disables just this term" knobs.
//
// error is SP−PV (DIRECT=FALSE, "reverse acting" — increasing PV should
// decrease CV, e.g. a heater) or PV−SP (DIRECT=TRUE, "direct acting" —
// increasing PV should increase CV, e.g. a chiller's cooling valve).
//
// The derivative acts on PV, not on error (IEC/OSCAT convention), so a
// setpoint step never "kicks" the D term — only a PV change does. It
// runs through a first-order filter with a fixed time constant KD/N
// (N=10) to keep sensor noise from being amplified into a wild CV.
//
// Anti-windup is conditional integration: the integral only accumulates
// when doing so would not push the *unclamped* output further past a
// rail it has already reached. That is cheap (no extra tuning knob
// beyond the clamp itself) and — unlike back-calculation — needs no
// separate tracking gain to document or mistune.
//
// AUTO/MANUAL is bumpless in both directions:
//   - In MANUAL, CV tracks CV_MAN directly, and the integral is
//     continuously back-solved each scan so that P+I+D would already
//     equal CV_MAN. On the scan AUTO goes TRUE, that preloaded integral
//     is used as-is (no fresh integration step is added on top of it),
//     so the auto-computed CV starts exactly where CV_MAN left off
//     instead of jumping; ordinary integration resumes the scan after.
//   - Derivative-on-PV means MANUAL never has to freeze or fake the D
//     term to avoid a kick — it keeps tracking PV the whole time.
//
// DT is the caller-supplied scan interval in seconds (bind a task's
// dt-tag, same as any hand-written PI in ST). Left at 0 (the default
// when unbound), the block falls back to measuring elapsed time between
// calls itself from the host clock — deterministic under the
// acceptance harness's virtual clock, exactly like TON's PT/ET.
//
// CV_MIN/CV_MAX default to the IEC 0..100 range when both are left
// unbound (0/0 — an explicit CV_MIN=CV_MAX=0 clamp range is otherwise
// pointless, since it would pin CV to zero always).
func registerPID() {
	// Slot layout — this order must match FBDef.AllSlots (Inputs ‖
	// Outputs ‖ InOuts ‖ Internals); the iota block below mirrors it
	// exactly so index arithmetic never has to be re-derived by hand.
	const (
		iAUTO = iota
		iPV
		iSP
		iKP
		iKI
		iKD
		iCVMAN
		iCVMIN
		iCVMAX
		iDIRECT
		iDT
		iDB
		iRESET
		oCV
		oERR
		oSATHI
		oSATLO
		oPTERM
		oITERM
		oDTERM
		nLASTMS
		nINTEGRAL
		nPREVPV
		nDFILT
		nHAVEPREV
		nPREVAUTO
	)

	RegisterFB(&FBDef{
		Name: "PID",
		Inputs: []FBSlot{
			{"AUTO", BoolT}, {"PV", RealT}, {"SP", RealT},
			{"KP", RealT}, {"KI", RealT}, {"KD", RealT},
			{"CV_MAN", RealT}, {"CV_MIN", RealT}, {"CV_MAX", RealT},
			{"DIRECT", BoolT}, {"DT", RealT}, {"DB", RealT}, {"RESET", BoolT},
		},
		Outputs: []FBSlot{
			{"CV", RealT}, {"ERR", RealT}, {"SAT_HI", BoolT}, {"SAT_LO", BoolT},
			{"P_TERM", RealT}, {"I_TERM", RealT}, {"D_TERM", RealT},
		},
		Internals: []FBSlot{
			{"_lastMs", TimeT}, {"_integral", RealT}, {"_prevPV", RealT},
			{"_dFilt", RealT}, {"_havePrev", BoolT}, {"_prevAuto", BoolT},
		},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			auto := inst.Slots[iAUTO].B
			pv := inst.Slots[iPV].F
			sp := inst.Slots[iSP].F
			kp := inst.Slots[iKP].F
			ki := inst.Slots[iKI].F
			kd := inst.Slots[iKD].F
			cvMan := inst.Slots[iCVMAN].F
			cvMin := inst.Slots[iCVMIN].F
			cvMax := inst.Slots[iCVMAX].F
			direct := inst.Slots[iDIRECT].B
			dtIn := inst.Slots[iDT].F
			db := inst.Slots[iDB].F
			reset := inst.Slots[iRESET].B

			// Default clamp range: an unbound CV_MIN/CV_MAX pair (both
			// still at the IEC zero default) means "use IEC 0..100".
			if cvMin == 0 && cvMax == 0 {
				cvMax = 100
			}
			if cvMin > cvMax {
				cvMin, cvMax = cvMax, cvMin
			}

			// Scan dt: explicit DT wins; otherwise measure elapsed host
			// time since the previous call (0 on the very first call).
			var dt float64
			lastMs := inst.Slots[nLASTMS].I
			switch {
			case dtIn > 0:
				dt = dtIn
			case lastMs != 0 && ctx.NowMs > lastMs:
				dt = float64(ctx.NowMs-lastMs) / 1000.0
			}
			inst.Slots[nLASTMS] = TimeVal(ctx.NowMs)

			// Error + deadband. Deadband only silences P/I chatter right
			// at setpoint; the derivative still sees every PV move.
			var errRaw float64
			if direct {
				errRaw = pv - sp
			} else {
				errRaw = sp - pv
			}
			errPID := errRaw
			if db > 0 && math.Abs(errRaw) <= db {
				errPID = 0
			}

			pTerm := kp * errPID

			// Derivative on PV, first-order filtered (fixed N=10).
			havePrev := inst.Slots[nHAVEPREV].B
			prevPV := inst.Slots[nPREVPV].F
			dFilt := inst.Slots[nDFILT].F
			rawDeriv := 0.0
			if havePrev && dt > 0 {
				rawDeriv = (pv - prevPV) / dt
			}
			if kd > 0 && dt > 0 {
				const n = 10.0
				tf := kd / n
				alpha := dt / (tf + dt)
				dFilt += alpha * (rawDeriv - dFilt)
			} else {
				dFilt = rawDeriv
			}
			inst.Slots[nPREVPV] = RealVal(pv)
			inst.Slots[nDFILT] = RealVal(dFilt)
			inst.Slots[nHAVEPREV] = BoolVal(true)
			derivSigned := dFilt
			if !direct {
				derivSigned = -dFilt
			}
			dTerm := kp * kd * derivSigned

			// Integral: bumpless-manual tracking, or conditional-
			// integration anti-windup while in AUTO.
			integral := inst.Slots[nINTEGRAL].F
			switch {
			case !auto:
				if kp != 0 && ki != 0 {
					integral = (cvMan - pTerm - dTerm) / (kp * ki)
				} else {
					integral = 0
				}
			case ki != 0 && dt > 0 && inst.Slots[nPREVAUTO].B:
				// Ordinary auto-in-auto scan: integrate one step, subject
				// to conditional-integration anti-windup. The very first
				// scan after a MANUAL->AUTO transition is deliberately
				// excluded (falls through with integral left untouched):
				// the MANUAL branch above already preloaded it, every
				// scan, so that P+I+D equals CV_MAN — integrating a fresh
				// step here on top of that preload is exactly what would
				// bump CV on the transition.
				candidate := integral + errPID*dt
				unclamped := pTerm + kp*ki*candidate + dTerm
				switch {
				case unclamped > cvMax && errPID > 0:
					// already saturated high, error still pushing higher: freeze
				case unclamped < cvMin && errPID < 0:
					// already saturated low, error still pushing lower: freeze
				default:
					integral = candidate
				}
			}
			if reset {
				integral = 0
			}
			inst.Slots[nINTEGRAL] = RealVal(integral)
			inst.Slots[nPREVAUTO] = BoolVal(auto)

			iTerm := kp * ki * integral

			var rawCV float64
			if auto {
				rawCV = pTerm + iTerm + dTerm
			} else {
				rawCV = cvMan
			}
			cv := rawCV
			satHi, satLo := false, false
			switch {
			case cv > cvMax:
				cv, satHi = cvMax, true
			case cv < cvMin:
				cv, satLo = cvMin, true
			}

			inst.Slots[oCV] = RealVal(cv)
			inst.Slots[oERR] = RealVal(errRaw)
			inst.Slots[oSATHI] = BoolVal(satHi)
			inst.Slots[oSATLO] = BoolVal(satLo)
			inst.Slots[oPTERM] = RealVal(pTerm)
			inst.Slots[oITERM] = RealVal(iTerm)
			inst.Slots[oDTERM] = RealVal(dTerm)
			return nil
		},
	})
}
