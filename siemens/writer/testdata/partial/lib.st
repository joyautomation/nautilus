FUNCTION_BLOCK SetLow
(* sets the low nibble of the byte it is given in-out *)
VAR_IN_OUT b : BYTE; END_VAR
b := b OR 16#0F;
END_FUNCTION_BLOCK
