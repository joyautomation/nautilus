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
