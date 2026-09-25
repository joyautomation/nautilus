PROGRAM FieldMap
(* Field-only tag glue — the one place this project departs from "the same
   program files run on the bench and in the field." `naut modbus import`
   names every tag `<instance-id>_<register-tag>` unconditionally (see
   modbus/codegen/devicemap.go); there is no way to suppress the prefix,
   so a bare name like `LSHH101` cannot come out of ANY instance id, and
   `P101`/`P102` are already spoken for by the VFD instances, so the RIO's
   seal-fail/over-temp points can't reuse them either. The RIO instance
   (devices.yaml, id RIO) therefore imports as RIO_Level, RIO_Flow,
   RIO_LSHH101, RIO_LSLL101, RIO_P101_SealFail, RIO_P101_OverTemp,
   RIO_P102_SealFail, RIO_P102_OverTemp — and this task, which only exists
   in field.yaml, renames them onto the canonical tags every other program
   (sequence.sfc, level.fbd, permissives.ld, stats.st) actually reads: the
   same names the bench manifest's sim task writes directly. Logged in
   docs/design/examples-dogfood.md. *)
VAR_EXTERNAL
    RIO_Level         : REAL;
    RIO_Flow          : REAL;
    RIO_LSHH101       : BOOL;
    RIO_LSLL101       : BOOL;
    RIO_P101_SealFail : BOOL;
    RIO_P101_OverTemp : BOOL;
    RIO_P102_SealFail : BOOL;
    RIO_P102_OverTemp : BOOL;

    LIT101_Level  : REAL;
    FIT101_Flow   : REAL;
    LSHH101       : BOOL;
    LSLL101       : BOOL;
    P101_SealFail : BOOL;
    P101_OverTemp : BOOL;
    P102_SealFail : BOOL;
    P102_OverTemp : BOOL;
END_VAR
LIT101_Level  := RIO_Level;
FIT101_Flow   := RIO_Flow;
LSHH101       := RIO_LSHH101;
LSLL101       := RIO_LSLL101;
P101_SealFail := RIO_P101_SealFail;
P101_OverTemp := RIO_P101_OverTemp;
P102_SealFail := RIO_P102_SealFail;
P102_OverTemp := RIO_P102_OverTemp;
END_PROGRAM
