PROGRAM Permissives
(* HOA, interlocks, the high-high override, and fail-to-run — everything
   that turns a "please run" request from the sequence into an actual
   RunCmd, or refuses to. Open the diagram: right-click -> "Open With ->
   Ladder Diagram". *)
VAR_EXTERNAL
    LeadIsP101 : BOOL;
    LeadReq    : BOOL;
    LagReq     : BOOL;
    LSHH101    : BOOL;
    LSLL101    : BOOL;
    LIT101_Level : REAL;
    LeadOffLevel : REAL;
    DryRunHold   : BOOL;

    P101_Req       : BOOL;
    P101_SealFail  : BOOL;
    P101_OverTemp  : BOOL;
    P101_VfdFault  : BOOL;
    P101_Permissive: BOOL;
    P101_Mode      : INT;
    P101_Running   : BOOL;
    P101_RunCmd    : BOOL;
    P101_InHand    : BOOL;
    P101_FailToRun : BOOL;
    P101_LockedOut : BOOL;
    P101_Avail     : BOOL;

    P102_Req       : BOOL;
    P102_SealFail  : BOOL;
    P102_OverTemp  : BOOL;
    P102_VfdFault  : BOOL;
    P102_Permissive: BOOL;
    P102_Mode      : INT;
    P102_Running   : BOOL;
    P102_RunCmd    : BOOL;
    P102_InHand    : BOOL;
    P102_FailToRun : BOOL;
    P102_LockedOut : BOOL;
    P102_Avail     : BOOL;

    ResetFaults : BOOL;
    BothUnavail : BOOL;
    LeadFailed  : BOOL;
    P101_DutyOk : BOOL;
    P102_DutyOk : BOOL;
    LeadUnavail : BOOL;
END_VAR
VAR
    m101 : MotorStarter;
    m102 : MotorStarter;
END_VAR
LD
  // duty mapping + high-high override, one rung: LSHH forces the call
  // regardless of the sequence or who's lead
  RUNG p101req
    [ LeadIsP101 LeadReq | /LeadIsP101 LagReq | LSHH101 ] ( P101_Req )

  RUNG p102req (* the same duty mapping for P-102: called when it is lead, or as lag *)
    [ /LeadIsP101 LeadReq | LeadIsP101 LagReq | LSHH101 ] ( P102_Req )

  // dry-run protection: a low-low trip holds both pumps off, in Hand as
  // well as Auto, until the well has refilled to Lead off. Restarting the
  // moment the float clears would run a pump dry, stop it and start it
  // again every few seconds.
  RUNG dryhold (* the low-low float latches the hold *)
    LSLL101 ( S DryRunHold )

  RUNG dryrelease (* refilled to Lead off, the float clear: the pumps may run again *)
    GE(LIT101_Level, LeadOffLevel) /LSLL101 ( R DryRunHold )

  RUNG p101perm (* a dry well (the hold) blocks both pumps, not just the lead *)
    /P101_SealFail /P101_OverTemp /P101_VfdFault /DryRunHold ( P101_Permissive )

  RUNG p102perm (* the same trips as P-101, against P-102's own devices *)
    /P102_SealFail /P102_OverTemp /P102_VfdFault /DryRunHold ( P102_Permissive )

  // every BOOL input bound by name: no free input for rung power, so the
  // call sits alone on the rail — see docs/functions.md "Power pins in ladder"
  RUNG p101start
    m101:MotorStarter(Mode := P101_Mode, AutoReq := P101_Req, Permissive := P101_Permissive,
                       RunFb := P101_Running, Reset := ResetFaults,
                       Run => P101_RunCmd, InHand => P101_InHand, FailToRun => P101_FailToRun,
                       LockedOut => P101_LockedOut)

  RUNG p102start (* P-102's starter: the same block, a second instance *)
    m102:MotorStarter(Mode := P102_Mode, AutoReq := P102_Req, Permissive := P102_Permissive,
                       RunFb := P102_Running, Reset := ResetFaults,
                       Run => P102_RunCmd, InHand => P102_InHand, FailToRun => P102_FailToRun,
                       LockedOut => P102_LockedOut)

  RUNG p101avail (* Off (Mode 0) is never available, same as a failed, locked-out, or blocked pump *)
    /P101_FailToRun /P101_LockedOut P101_Permissive /EQ(P101_Mode, 0) ( P101_Avail )

  RUNG p102avail (* as p101avail, for P-102 *)
    /P102_FailToRun /P102_LockedOut P102_Permissive /EQ(P102_Mode, 0) ( P102_Avail )

  RUNG bothunavail (* neither pump can take a call: the station alarm *)
    /P101_Avail /P102_Avail ( BothUnavail )

  RUNG leadfailed (* whichever pump is currently lead, reflected for the sequence *)
    [ LeadIsP101 P101_FailToRun | /LeadIsP101 P102_FailToRun ] ( LeadFailed )

  // Fit to be lead: in Auto (the sequence commands it) and available. A
  // pump in Hand runs on the operator's say-so, so it is not.
  RUNG p101duty
    EQ(P101_Mode, 2) P101_Avail ( P101_DutyOk )

  RUNG p102duty (* as p101duty, for P-102 *)
    EQ(P102_Mode, 2) P102_Avail ( P102_DutyOk )

  RUNG leadunavail (* the lead is no longer fit while the other is: hand it over *)
    [ LeadIsP101 /P101_DutyOk P102_DutyOk | /LeadIsP101 /P102_DutyOk P101_DutyOk ] ( LeadUnavail )
END_LD
END_PROGRAM
