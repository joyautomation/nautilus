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

  RUNG p102req
    [ /LeadIsP101 LeadReq | LeadIsP101 LagReq | LSHH101 ] ( P102_Req )

  RUNG p101perm (* a dry well (LSLL) blocks both pumps, not just the lead *)
    /P101_SealFail /P101_OverTemp /P101_VfdFault /LSLL101 ( P101_Permissive )

  RUNG p102perm
    /P102_SealFail /P102_OverTemp /P102_VfdFault /LSLL101 ( P102_Permissive )

  // every BOOL input bound by name: no free input for rung power, so the
  // call sits alone on the rail — see docs/functions.md "Power pins in ladder"
  RUNG p101start
    m101:MotorStarter(Mode := P101_Mode, AutoReq := P101_Req, Permissive := P101_Permissive,
                       RunFb := P101_Running, Reset := ResetFaults,
                       Run => P101_RunCmd, InHand => P101_InHand, FailToRun => P101_FailToRun,
                       LockedOut => P101_LockedOut)

  RUNG p102start
    m102:MotorStarter(Mode := P102_Mode, AutoReq := P102_Req, Permissive := P102_Permissive,
                       RunFb := P102_Running, Reset := ResetFaults,
                       Run => P102_RunCmd, InHand => P102_InHand, FailToRun => P102_FailToRun,
                       LockedOut => P102_LockedOut)

  RUNG p101avail (* Off (Mode 0) is never available, same as a failed, locked-out, or blocked pump *)
    /P101_FailToRun /P101_LockedOut P101_Permissive /EQ(P101_Mode, 0) ( P101_Avail )

  RUNG p102avail
    /P102_FailToRun /P102_LockedOut P102_Permissive /EQ(P102_Mode, 0) ( P102_Avail )

  RUNG bothunavail
    /P101_Avail /P102_Avail ( BothUnavail )

  RUNG leadfailed (* whichever pump is currently lead, reflected for the sequence *)
    [ LeadIsP101 P101_FailToRun | /LeadIsP101 P102_FailToRun ] ( LeadFailed )
END_LD
END_PROGRAM
