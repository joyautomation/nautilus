PROGRAM Transfer
(* Handshake interlocks with the line PLC, the transfer pump's permissive
   and seal-in, the CIP-due check, and the handshake timeout alarm.
   phases.sfc only ever asks (TransferReq, AgitateReq); this program
   decides whether the pump, the line request, and the agitator command
   actually assert — the same split lift-station keeps between
   sequence.sfc and permissives.ld. AG201_Cmd is AgitateReq ANDed with
   the recipe's own Agitate flag: AgitateReq is N-qualified true only
   while Agitate/Heat/Hold are active in phases.sfc (self-clearing
   everywhere else, including Transfer/Idle and every non-agitate
   recipe), so this rung is the only writer AG201_Cmd ever has — no
   separate reset on Abort is needed. Open the diagram: right-click ->
   "Open With -> Ladder Diagram". *)
VAR_EXTERNAL
    TransferReq      : BOOL;
    Line_Ready       : BOOL;
    Line_Fault       : BOOL;
    Line_Accept      : BOOL;
    Line_Request     : BOOL;
    XV203_Open       : BOOL;
    XV203_Cmd        : BOOL;
    P202_Cmd         : BOOL;
    P202_Permissive  : BOOL;
    LT201_Level      : REAL;
    Batch            : BatchStatus;
    Active           : Recipe;
    AgitateReq       : BOOL;
    AG201_Cmd        : BOOL;
    CipEveryN        : INT;
    CipDue           : BOOL;
    HandshakeTimeout : BOOL;
END_VAR
VAR
    tmo : TON;
END_VAR
LD
  RUNG xv203 (* the transfer valve opens the moment the sequence asks *)
    TransferReq ( XV203_Cmd )

  RUNG linereq (* and the line sees the same request, unconditionally *)
    TransferReq ( Line_Request )

  RUNG permissive (* the line is ready, not faulted, and our valve confirms open *)
    Line_Ready /Line_Fault XV203_Open ( P202_Permissive )

  RUNG p202seal (* seal in on the request+permissive; hold until the tank drains *)
    [ TransferReq P202_Permissive | P202_Cmd ] /LE(LT201_Level, 2.0) ( P202_Cmd )

  RUNG cipdue (* every CipEveryN-th batch needs a flush after this transfer *)
    EQ(MOD(Batch.Count, CipEveryN), 0) ( CipDue )

  RUNG agitate (* AgitateReq (phases.sfc: on while Agitate/Heat/Hold are active) ANDed with the recipe's own flag — off in Transfer/Idle and every non-agitate recipe, with no separate reset *)
    AgitateReq Active.Agitate ( AG201_Cmd )

  RUNG handshaketmo (* requested, not yet accepted, for 30 s straight *)
    Line_Request /Line_Accept tmo:TON(PT := T#30S) ( HandshakeTimeout )
END_LD
END_PROGRAM
