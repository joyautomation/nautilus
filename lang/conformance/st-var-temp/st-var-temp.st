PROGRAM TempSt
(* Two calls of one instance per scan; n is the PROGRAM's own temp. *)
VAR_EXTERNAL
    st_Fresh1 : INT;
    st_Fresh2 : INT;
    st_Total  : INT;
    st_Scan   : INT;
END_VAR
VAR
    s : Scratch;
END_VAR
VAR_TEMP
    n : INT;
END_VAR
s(Add := 1, Fresh => st_Fresh1);
s(Add := 2, Fresh => st_Fresh2, Total => st_Total);
n := n + 1;
st_Scan := n;
END_PROGRAM
