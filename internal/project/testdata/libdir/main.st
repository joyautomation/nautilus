PROGRAM Main
VAR_EXTERNAL
    Start : BOOL;
    Stop : BOOL;
    Run : BOOL;
    Level : REAL;
    High : BOOL;
END_VAR
VAR
    seq : PumpSeq;      (* lib/rungs.ld *)
    tank : TankModel;   (* lib/physics/tank.st *)
    hi : HiAlarm;       (* lib/blocks.st *)
    snap : TankState;   (* lib/physics/tank.st *)
END_VAR
seq(Start := Start, Stop := Stop);
Run := seq.Run;
tank(Fill := Run);
Level := tank.Level;
snap.Level := Level;
hi(PV := snap.Level, Limit := 5.0);
High := hi.Alm;
END_PROGRAM
