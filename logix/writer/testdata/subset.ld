PROGRAM MainProgram
(* Every construct of the Logix v1 subset, in one program: the golden
   snapshot and the structural round trip both run over this file. *)
VAR_EXTERNAL
    Start  : BOOL;
    Stop   : BOOL;
    Run    : BOOL;
    Auto   : BOOL;
    Remote : BOOL;
    Local  : BOOL;
    Level  : REAL;
    HiSP   : REAL;
    Counts : DINT;
    Target : INT;
    Fault  : BOOL;
    Ready  : BOOL;
    Maint  : BOOL;
END_VAR
VAR
    t1 : TON;
    t2 : TOF;
    t3 : TON;
    c1 : CTU;
    c2 : CTU;
    Dwell  : TIME := T#2S;
    Steps  : SINT := 16#0F;
    Big    : LREAL := 1.5e3;
    Levels : ARRAY [0..3] OF REAL;
    Bits   : ARRAY [0..31] OF BOOL;
    Reset    : BOOL;
    Done     : BOOL;
    Dwelling : BOOL;
    Pulse    : BOOL;
    Dropped  : BOOL;
    Over     : BOOL := TRUE;
END_VAR
LD
  // a note above the rung travels into the Logix rung comment
  RUNG seal (* seal-in with a stop *)
    [ Start | Run ] /Stop ( Run )

  RUNG alarm
    GE(Level, HiSP) /EQ(Counts, 0) ( Fault )

  RUNG nested (* nested branches, a one-shot at the head *)
    +Start [ Remote [ /Auto | Bits[1] ] | Local ] ( Ready )

  RUNG edges (* edges away from the head take a helper rung each *)
    Auto +Local -Remote ( Pulse )

  RUNG dwell (* timer in series: power continues from the done bit *)
    Run t1:TON(PT := T#10S) ( Dwelling )

  RUNG off (* a TOF ends its rung; what follows moves to a continuation *)
    Run t2:TOF(PT := Dwell) /Fault ( Dropped )

  RUNG count (* counter with a reset and a variable preset *)
    +Done c1:CTU(PV := Target, R := Reset) ( S Over ) ( R Maint )

  RUNG count2
    Done c2:CTU(PV := 3) ( Done )

  RUNG members (* rewritten members *)
    [ t1.Q | /c1.Q ] GT(t1.ET, 500) LT(c1.CV, Target) Bits[3] ( Maint )

  RUNG tail (* a block as the rung's last element *)
    Fault t3:TON(PT := T#500MS)
END_LD
END_PROGRAM
