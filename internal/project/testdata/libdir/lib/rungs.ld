FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
LD
  RUNG seal (* the seal-in: Start latches Run until Stop *) [ Start | Run ] /Stop ( Run )
END_LD
END_FUNCTION_BLOCK
