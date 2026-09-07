PROGRAM Annunciate
(* The bits, computed in control logic at scan rate. That is the whole
   division of labour: comparing a measurement to a limit is control, and
   deciding whether a person has to be told about it — and whether they
   have seen it yet — is alarm management, which lives in the host's alarm
   engine, not here. Nothing in this program knows an alarm engine exists. *)
VAR_EXTERNAL
    RTU9_WEL15_FIT_001 : AnalogInput;
    RTU9_TNK01_LIT_001 : AnalogInput;

    FlowHH  : REAL;  FlowH  : REAL;  FlowL  : REAL;  FlowLL  : REAL;
    LevelHH : REAL;  LevelH : REAL;  LevelL : REAL;  LevelLL : REAL;

    RTU9__Online : BOOL;
    RTU9_AnyAlm  : BOOL;
END_VAR

RTU9_WEL15_FIT_001.HH := RTU9_WEL15_FIT_001.PV > FlowHH;
RTU9_WEL15_FIT_001.H  := RTU9_WEL15_FIT_001.PV > FlowH;
RTU9_WEL15_FIT_001.L  := RTU9_WEL15_FIT_001.PV < FlowL;
RTU9_WEL15_FIT_001.LL := RTU9_WEL15_FIT_001.PV < FlowLL;

RTU9_TNK01_LIT_001.HH := RTU9_TNK01_LIT_001.PV > LevelHH;
RTU9_TNK01_LIT_001.H  := RTU9_TNK01_LIT_001.PV > LevelH;
RTU9_TNK01_LIT_001.L  := RTU9_TNK01_LIT_001.PV < LevelL;
RTU9_TNK01_LIT_001.LL := RTU9_TNK01_LIT_001.PV < LevelLL;

(* The site rollup an overview screen shows. It is gated on the node being
   online for the same reason every alarm definition is: a dead edge node
   holds its last values, so an ungated rollup would stay lit forever. *)
RTU9_AnyAlm := RTU9__Online AND (
      RTU9_WEL15_FIT_001.HH OR RTU9_WEL15_FIT_001.L
   OR RTU9_TNK01_LIT_001.HH OR RTU9_TNK01_LIT_001.L);

END_PROGRAM
