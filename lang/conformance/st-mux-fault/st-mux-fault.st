PROGRAM MuxFaultSt
(* MUX with a direct selector (K, which the suite keeps in 0..2) and with
   the documented guard (W clamped by LIMIT). A K outside 0..2 faults the
   scan; that half is asserted in ../faults_test.go because a faulting
   scan fails any YAML test that causes it. *)
VAR_EXTERNAL
    K, W : INT;
    st_Pick, st_Safe, st_Scans : INT;
END_VAR

st_Pick := MUX(K, 10, 20, 30);
st_Safe := MUX(LIMIT(0, W, 2), 10, 20, 30);
st_Scans := st_Scans + 1;
END_PROGRAM
