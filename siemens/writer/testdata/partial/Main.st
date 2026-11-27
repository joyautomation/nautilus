PROGRAM Main
(* #1305 B3: partial access, as the Siemens writer writes it, and the
   #1429 long LREAL literals in every place they are written. Also the B3
   bench program (fast task, 50 ms). *)
VAR_EXTERNAL
    Dx : DINT;            (* 16#01020304 *)
    Neg, Top : DINT;      (* -1, 16#80000000 *)
    Lx : LINT;            (* set below: a manifest init above 2^53 loses digits in nautilus *)
    Wx : WORD;            (* 16#1234 *)
    Bx : BYTE;            (* 16#5A *)
    Ix : INT;             (* -2 *)
    Dx2 : DINT;           (* 16#0000C864: bytes 100, 200 *)
    rBf : BYTE; rIw : WORD; rGt : BOOL; rIo : DINT;
    Sh : DWORD;           (* shared: Main writes byte 0, Slow byte 1 *)
    FastN : DINT;
    rB0, rB1, rB2, rB3, rFl : BYTE;
    rW0, rW1, rWm : WORD;
    rD0 : DWORD;
    rDw, rNg : DINT;
    rLm : LINT;
    rS : SINT;
    rX11 : BOOL;
    rL0 : LWORD;
    kStore, kBin, kCall, kAtan, kTemp, kNeg : LWORD;   (* #1429: the bits *)
END_VAR
VAR
    dw, ng : DINT;
    lm : LINT;
    wm : WORD;
    lx2 : LREAL := 1.5;
    io : SetLow;
    dio : DINT;
    lp, lq, lr, la, ln : LREAL;
END_VAR
VAR_TEMP
    tt : LREAL := 2.718281828459045;
END_VAR
Lx := 16#0102030405060708;
(* reads, LSB-first *)
rB0 := Dx.%B0; rB1 := Dx.%B1; rB2 := Dx.%B2; rB3 := Dx.%B3;
rW0 := Dx.%W0; rW1 := Dx.%W1; rD0 := Dx.%D0;
rFl := Dx.%W1.%B0;          (* a part of a part: Dx.%B2 *)
rX11 := Dx.%B1.%X3;         (* bit 11 *)
rL0 := Lx.%L0;
(* writes *)
dw := Dx; dw.%B1 := 16#AA; rDw := dw;
ng := Neg; ng.%B3 := 16#7F; rNg := ng;
lm := 0; lm.%D1 := Dx.%D0; rLm := lm;
wm := Wx; wm.%B1 := 16#55; rWm := wm;
rS := BYTE_TO_SINT(Top.%B3);
(* a slice as wide as its variable; a part in 64-bit arithmetic; a slice as an in-out *)
rBf := Bx.%B0;
rIw := Ix.%W0;
rGt := Dx2.%B0 + Dx2.%B1 > 255;
dio := Dx;
io(b := dio.%B2);
rIo := dio;
(* the shared tag: this task's byte *)
FastN := FastN + 1;
Sh.%B0 := DINT_TO_BYTE(FastN MOD 256);
(* #1429: a long LREAL literal in each place it is written *)
lp := 3.141592653589793;
lq := lx2 * 2.718281828459045;
lr := MAX(lx2, 1.4142135623730951);
la := ATAN2(1.4142135623730951, lx2);
ln := -3.141592653589793;
kStore := LREAL_TO_LWORD(lp); kBin := LREAL_TO_LWORD(lq); kCall := LREAL_TO_LWORD(lr);
kAtan := LREAL_TO_LWORD(la); kTemp := LREAL_TO_LWORD(tt); kNeg := LREAL_TO_LWORD(ln);
END_PROGRAM
