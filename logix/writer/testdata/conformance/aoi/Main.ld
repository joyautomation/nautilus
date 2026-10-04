PROGRAM Starters
(* Two motor starters from one block. Every BOOL input is bound by name,
   so each call sits alone on the rail — the lift station's own shape. *)
VAR_EXTERNAL
    P101_Mode       : INT;
    P101_Req        : BOOL;
    P101_Running    : BOOL;
    P101_Permissive : BOOL;
    P102_Mode       : INT;
    P102_Req        : BOOL;
    P102_Running    : BOOL;
    P102_Permissive : BOOL;
    ResetFaults     : BOOL;
    P101_RunCmd     : BOOL;
    P101_InHand     : BOOL;
    P101_FailToRun  : BOOL;
    P101_LockedOut  : BOOL;
    P102_RunCmd     : BOOL;
    P102_InHand     : BOOL;
    P102_FailToRun  : BOOL;
    P102_LockedOut  : BOOL;
END_VAR
VAR
    m101 : MotorStarter;
    m102 : MotorStarter;
END_VAR
LD
  RUNG p101
    m101:MotorStarter(Mode := P101_Mode, AutoReq := P101_Req, Permissive := P101_Permissive,
                      RunFb := P101_Running, Reset := ResetFaults,
                      Run => P101_RunCmd, InHand => P101_InHand, FailToRun => P101_FailToRun,
                      LockedOut => P101_LockedOut)

  RUNG p102
    m102:MotorStarter(Mode := P102_Mode, AutoReq := P102_Req, Permissive := P102_Permissive,
                      RunFb := P102_Running, Reset := ResetFaults,
                      Run => P102_RunCmd, InHand => P102_InHand, FailToRun => P102_FailToRun,
                      LockedOut => P102_LockedOut)
END_LD
END_PROGRAM
