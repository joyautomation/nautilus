PROGRAM main
(* Every STRING construct the Siemens writer writes (#1303). *)
VAR_EXTERNAL
    Msg : STRING;
    Go : BOOL;
    N : DINT;
END_VAR
VAR
    s, t, r : STRING;
    named : STRING := 'T#5s';
    l, p, ln, k : INT;
    d : DINT;
    li : LINT;
    us : USINT;
    ui : UINT;
    ul : ULINT;
    x : REAL;
    lx : LREAL;
    b, eq : BOOL;
END_VAR

(* the functions, arguments that need no clamp *)
r := LEFT(s, l);
r := RIGHT(s, l);
r := MID(s, l, 2);
ln := LEN(s);
k := FIND(s, 'LL');
(* clamped where TIA gives '' and nautilus clamps *)
r := MID(s, l, p);
r := INSERT(s, 'XY', p);
r := DELETE(s, l, p);
r := REPLACE(s, 'ab', l, p);
(* constants clamped here *)
r := MID(s, 2, 0);
r := INSERT(s, 'XY', -2);
r := DELETE(s, -1, 2);
r := LEFT(s, 999);
(* 64-bit lengths and positions *)
r := LEFT(s, LEN(s) - 1);
r := MID(s, d, li);
r := RIGHT(s, us);
r := DELETE(s, ui, p + 1);
(* a CONCAT chain is one call *)
r := CONCAT(CONCAT(s, '-'), t, CONCAT('<', CONCAT(r, '>')));
r := CONCAT('a', 'b');
(* inside other functions, compared, selected *)
IF LEN(LEFT(s, 3)) > 0 AND FIND(CONCAT(s, t), 'x') = 0 THEN
    r := SEL(Go, '', CONCAT(s, ','));
END_IF;
r := MUX(k, s, t, 'none');
eq := s = t;
eq := s <> '';
(* conversions *)
r := INT_TO_STRING(l);
r := DINT_TO_STRING(d);
r := LINT_TO_STRING(li);
r := USINT_TO_STRING(us);
r := UINT_TO_STRING(ui);
r := ULINT_TO_STRING(ul);
r := TO_STRING(N);
r := BOOL_TO_STRING(b);
l := STRING_TO_INT(s);
d := STRING_TO_DINT(s);
x := STRING_TO_REAL(s);
lx := STRING_TO_LREAL(s);
(* literals *)
r := '';
r := 'it$'s $$5$N';
Msg := CONCAT(Msg, named);
N := N + 1;
END_PROGRAM
