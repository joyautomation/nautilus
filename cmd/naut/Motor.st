(* Shared PLC-Lab input mapping for current and previous scan images.
   Byte indices are absolute I bytes; masks identify the bit within each byte. *)
FUNCTION_BLOCK MapInputs
VAR_INPUT
    Bytes : ARRAY[0..3] OF BYTE;
END_VAR
VAR_OUTPUT
    S0_EStop : BOOL;
    S1_ControlOn : BOOL;
    S2_ControlOff : BOOL;
    S3_Start : BOOL;
    S4_Pause : BOOL;
    S5_ClearEStop : BOOL;
    Reserve1 : BOOL;
    Reserve2 : BOOL;
    B4_ItemOnEntry8ft : BOOL;
    B5_ItemOnEntry10ft : BOOL;
    B6_ItemOnEntry12ft : BOOL;
    B7_ItemOnEntry16ft : BOOL;
    B8_ItemInStorage8ft : BOOL;
    B9_ItemInStorage10ft : BOOL;
    B10_ItemInStorage12ft : BOOL;
    B11_ItemInStorage16ft : BOOL;
    ReserveByte : BOOL;
    B0_New8ft : BOOL;
    B1_New10ft : BOOL;
    B2_New12ft : BOOL;
    B3_New16ft : BOOL;
END_VAR
S0_EStop := (Bytes[0] AND 1) <> 0;
S1_ControlOn := (Bytes[0] AND 2) <> 0;
S2_ControlOff := (Bytes[0] AND 4) <> 0;
S3_Start := (Bytes[0] AND 8) <> 0;
S4_Pause := (Bytes[0] AND 16) <> 0;
S5_ClearEStop := (Bytes[0] AND 32) <> 0;
Reserve1 := (Bytes[0] AND 64) <> 0;
Reserve2 := (Bytes[0] AND 128) <> 0;
B4_ItemOnEntry8ft := (Bytes[1] AND 1) <> 0;
B5_ItemOnEntry10ft := (Bytes[1] AND 2) <> 0;
B6_ItemOnEntry12ft := (Bytes[1] AND 4) <> 0;
B7_ItemOnEntry16ft := (Bytes[1] AND 8) <> 0;
B8_ItemInStorage8ft := (Bytes[1] AND 16) <> 0;
B9_ItemInStorage10ft := (Bytes[1] AND 32) <> 0;
B10_ItemInStorage12ft := (Bytes[1] AND 64) <> 0;
B11_ItemInStorage16ft := (Bytes[1] AND 128) <> 0;
ReserveByte := (Bytes[2] AND 1) <> 0;
B0_New8ft := (Bytes[3] AND 1) <> 0;
B1_New10ft := (Bytes[3] AND 2) <> 0;
B2_New12ft := (Bytes[3] AND 4) <> 0;
B3_New16ft := (Bytes[3] AND 8) <> 0;
END_FUNCTION_BLOCK
(* The connector PLC-Lab reads and writes over OPC UA. PLC-Lab looks for a
   container named exactly PlcLabConnector holding these members, with these
   names and types: on CODESYS it is a global variable list; here it is one
   struct tag of this type (nautilus.yaml). See README.md.

   PLC-Lab writes Inputs (its sensors, as bytes) and the two area tables,
   which say which bytes it uses; it reads Outputs. The SIZE_ members tell it
   how big the arrays are. *)

TYPE
  PlcLabArea : STRUCT
    CntBytes : WORD;  (* how many bytes; 0 ends the table *)
    Addr     : WORD;  (* first byte in Inputs or Outputs *)
    IoOfs    : WORD;  (* first byte of the process image: 0 is IB0 / QB0 *)
  END_STRUCT;

  PlcLabConnectorData : STRUCT
    SIZE_AREA_I_ARRAY : WORD := 10;
    SIZE_AREA_Q_ARRAY : WORD := 10;
    SIZE_I_ARRAY      : WORD := 1024;
    SIZE_Q_ARRAY      : WORD := 1024;
    Inputs      : ARRAY[0..1023] OF BYTE;      (* written by PLC-Lab *)
    Outputs     : ARRAY[0..1023] OF BYTE;      (* read by PLC-Lab *)
    AreaInputs  : ARRAY[0..9] OF PlcLabArea;   (* written by PLC-Lab *)
    AreaOutputs : ARRAY[0..9] OF PlcLabArea;   (* written by PLC-Lab *)
  END_STRUCT;
END_TYPE
PROGRAM Motor
(* MTR-101: constant speed motor on a Hand-Off-Auto selector.
   HAND runs it now, AUTO runs it when MTR101_AutoReq asks, OFF (neither
   contact made) stops it. Both modes honour the overload and the
   fail-to-run fault. *)
LD
  RUNG permissive (* never run into a tripped overload or an unacknowledged fail-to-run *)
    /MTR101_Overload /MTR101_FailToRun ( MTR101_Permissive )

  RUNG runcmd (* HAND bypasses the process request, not the protection; HAND wins if both selector contacts read made *)
    [ MTR101_HandSel | MTR101_AutoSel MTR101_AutoReq ] MTR101_Permissive ( MTR101_RunCmd )

  RUNG failtorun (* commanded but no aux contact within 5 s: starter or motor failed; latch it so it is seen *)
    MTR101_RunCmd /MTR101_RunFbk ftrTmr:TON(PT := T#5S) ( S MTR101_FailToRun )

  RUNG faultreset (* operator acknowledges the fault; a held button resets only once *)
    +MTR101_Reset ( R MTR101_FailToRun )
END_LD
END_PROGRAM
