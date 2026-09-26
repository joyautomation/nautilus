(* Bench-only plant model: one dosing line's flow/valve-travel dynamic and
   the mix tank's level/temperature balance. Two FUNCTION_BLOCKs, no
   PROGRAM — a project library, the same rule lift-station's physics.st
   follows. sim.st is the only caller; line.yaml has no sim task, so these
   sit compiled but unused there, same as lift-station's field.yaml. *)

FUNCTION_BLOCK DoseLine
(* One dosing line: 20 L/min while its valve command is on, with a 0.5 s
   valve-travel delay before flow (and the Open feedback) appears — the
   same TON-gated feedback shape as lift-station's VfdModel. Literals
   inlined rather than named in a VAR CONSTANT block: a VAR CONSTANT
   referenced from a body whose outputs are captured at the call site
   (VAR_OUTPUT via `=>`) has silently dropped the write-back in this
   project's own testing, reproduced and logged in
   docs/design/examples-dogfood.md; the workaround is to inline the
   literal instead of naming it. *)
VAR_INPUT
    Cmd : BOOL;
    Dt  : REAL; (* seconds, unused directly — travel is timed in ms *)
END_VAR
VAR_OUTPUT
    Flow : REAL; (* L/min *)
    Open : BOOL; (* valve-open feedback *)
END_VAR
VAR
    travel : TON;
END_VAR
travel(IN := Cmd, PT := T#500MS);
Open := travel.Q;
IF Open THEN
    Flow := 20.0;
ELSE
    Flow := 0.0;
END_IF;
END_FUNCTION_BLOCK

FUNCTION_BLOCK JacketedTank
(* MT-201: level from a dose-in/transfer-out balance (500 L over the full
   0..100 % span, so 5 L moves the level 1 %) and temperature toward the
   jacket water temperature at a rate set by the control valve's position,
   less a small loss to ambient. Same VAR CONSTANT caveat as DoseLine
   above — every tuning number here is a bare literal on purpose. *)
VAR_INPUT
    DoseAFlow    : REAL; (* L/min, into the tank *)
    DoseBFlow    : REAL; (* L/min, into the tank *)
    TransferFlow : REAL; (* L/min, out of the tank *)
    TCVPos       : REAL; (* %, jacket valve position *)
    JacketTempC  : REAL; (* hot water supply temperature *)
    AmbientC     : REAL;
    Dt           : REAL; (* seconds *)
END_VAR
VAR_IN_OUT
    Level : REAL; (* %, 0..100, 500 L span *)
    Temp  : REAL; (* degC *)
END_VAR
Level := LIMIT(0.0, Level + (DoseAFlow + DoseBFlow - TransferFlow) * (Dt / 60.0) / 5.0, 100.0);
Temp  := Temp + ((JacketTempC - Temp) * (TCVPos / 100.0) * 0.01 - (Temp - AmbientC) * 0.002) * Dt;
END_FUNCTION_BLOCK
