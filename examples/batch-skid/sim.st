PROGRAM Skid
(* Bench-only plant: MT-201 (JacketedTank, lib/physics.st) fed by two
   dosing lines (DoseLine x2), the transfer pump and valve feedbacks, and
   a stand-in for "the line" — the existing Logix PLC's receiving tank —
   so a full batch closes the loop with nothing wired up. line.yaml has
   no sim task: Line_Ready/Line_Accept/Line_TankLevel/Line_Fault come from
   `naut logix emulate`/a real controller instead, over the eip driver,
   and the skid's own field values are the field's job once this project
   grows a local I/O driver (out of scope here — see README.md). *)
VAR_EXTERNAL
    SimDtS : REAL;

    XV201A_Cmd : BOOL;
    XV201A_Open : BOOL;
    FIT201A_Flow : REAL;
    XV201B_Cmd : BOOL;
    XV201B_Open : BOOL;
    FIT201B_Flow : REAL;
    AG201_Cmd : BOOL;
    AG201_Running : BOOL;
    P202_Cmd : BOOL;
    P202_Running : BOOL;
    XV203_Cmd : BOOL;
    XV203_Open : BOOL;
    TCV201_Pos : REAL;
    LT201_Level : REAL;
    TT201_Temp : REAL;

    Line_Ready : BOOL;
    Line_Accept : BOOL;
    Line_TankLevel : REAL;
    Line_Fault : BOOL;
    Line_Request : BOOL;
    InjectLineFault : BOOL;
END_VAR
VAR
    tank     : JacketedTank;
    doseA    : DoseLine;
    doseB    : DoseLine;
    xv203Tmr : TON;
    p202Tmr  : TON;
    ag201Tmr : TON;
    acceptTmr: TON;
    xferLps  : REAL;
END_VAR

doseA(Cmd := XV201A_Cmd, Dt := SimDtS, Flow => FIT201A_Flow, Open => XV201A_Open);
doseB(Cmd := XV201B_Cmd, Dt := SimDtS, Flow => FIT201B_Flow, Open => XV201B_Open);

xv203Tmr(IN := XV203_Cmd, PT := T#500MS);
XV203_Open := xv203Tmr.Q;

p202Tmr(IN := P202_Cmd, PT := T#500MS);
P202_Running := p202Tmr.Q;

ag201Tmr(IN := AG201_Cmd, PT := T#500MS);
AG201_Running := ag201Tmr.Q;

IF P202_Running THEN
    xferLps := 60.0;
ELSE
    xferLps := 0.0;
END_IF;

tank(DoseAFlow := FIT201A_Flow, DoseBFlow := FIT201B_Flow,
     TransferFlow := xferLps, TCVPos := TCV201_Pos,
     JacketTempC := 90.0, AmbientC := 20.0, Dt := SimDtS,
     Level := LT201_Level, Temp := TT201_Temp);

(* The line: a receiving tank on the other end of XV-203. Ready while it
   has headroom; Accept follows a request after a 2 s settling delay, the
   same shape `naut logix emulate`'s Receive routine (line/Line.L5X) uses,
   so the bench and the emulated line behave alike. *)
Line_Ready := Line_TankLevel < 80.0;
acceptTmr(IN := Line_Request AND NOT InjectLineFault, PT := T#2S);
Line_Accept := acceptTmr.Q;
Line_Fault := InjectLineFault;
Line_TankLevel := LIMIT(0.0, Line_TankLevel + xferLps * SimDtS / 60.0 / 10.0 - 0.02 * SimDtS, 100.0);
END_PROGRAM
