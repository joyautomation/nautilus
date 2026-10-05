PROGRAM Conveyor
(* The main routine. Each motor is one rung calling MotorStarter (the
   JSR habit, as a block call); +Tag is the ONS. *)
VAR_EXTERNAL
    EStop        : BOOL;
    AirOk        : BOOL;
    FaultReset   : BOOL;
    M1_StartPB   : BOOL;
    M1_StopPB    : BOOL;
    M1_Aux       : BOOL;
    M1_OL        : BOOL;
    M1_Run       : BOOL;
    M2_StartPB   : BOOL;
    M2_StopPB    : BOOL;
    M2_Aux       : BOOL;
    M2_OL        : BOOL;
    M2_Run       : BOOL;
    M2_SpeedRef  : REAL;
    M1_AuxBypass : BOOL;
    Maint        : BOOL;
    CountReset   : BOOL;
    M2_SpeedPct  : REAL;
    MinHz        : REAL;
    MaxHz        : REAL;
    M1_Permit    : BOOL;
    M2_Permit    : BOOL;
    M1_Starts    : INT;
    Alm_EStop    : BOOL;
    Alm_M1Fault  : BOOL;
    Alm_M2Fault  : BOOL;
    Alm_M1FTS    : BOOL;
    Alm_M2FTS    : BOOL;
    Alm_Any      : BOOL;
END_VAR
VAR
    m1      : MotorStarter;
    m2      : MotorStarter;
    cStarts : CTU;
    spd     : SpeedCalc;
END_VAR
LD
  RUNG m1perm
    /EStop AirOk ( M1_Permit )

  RUNG m1
    +M1_StartPB m1:MotorStarter(Stop := M1_StopPB, Permit := M1_Permit, Aux := M1_Aux, Fault := M1_OL, Reset := FaultReset, FailToStart => Alm_M1FTS) ( M1_Run )

  RUNG m2perm
    /EStop [ M1_Run [ M1_Aux | M1_AuxBypass ] | Maint ] ( M2_Permit )

  RUNG m2
    +M2_StartPB m2:MotorStarter(Stop := M2_StopPB, Permit := M2_Permit, Aux := M2_Aux, Fault := M2_OL, Reset := FaultReset, FailToStart => Alm_M2FTS) ( M2_Run )

  RUNG starts
    M1_Run cStarts:CTU(R := CountReset, PV := 9999, CV => M1_Starts)

  RUNG speed
    M2_Run spd:SpeedCalc(Pct := M2_SpeedPct, MinHz := MinHz, MaxHz := MaxHz, Hz => M2_SpeedRef)

  RUNG almestop
    EStop ( Alm_EStop )

  RUNG almm1
    m1.Faulted ( Alm_M1Fault )

  RUNG almm2
    m2.Faulted ( Alm_M2Fault )

  RUNG almany
    [ Alm_EStop | Alm_M1Fault | Alm_M2Fault ] ( Alm_Any )
END_LD
END_PROGRAM
