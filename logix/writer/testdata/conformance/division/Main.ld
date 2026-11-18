PROGRAM Main
(* An integer / on a rung, in the forms the writer writes: DIV and CPT into
   an integer, a CMP against an integer, and a quotient that reaches a REAL
   through a DINT tag. Each truncates toward zero on both runtimes. The
   forms that meet a REAL (a REAL destination, a REAL operand beside the
   quotient, a CMP against a REAL) divide in REAL on the controller and are
   refused by logix/conversion (convert_test.go, #368). *)
VAR_EXTERNAL
    a  : DINT;
    b  : DINT;
    c  : DINT;
    x  : REAL;
    dq : DINT;
    iq : INT;
    dc : DINT;
    q  : DINT;
    rq : REAL;
    rr : REAL;
    gi : BOOL;
    gx : BOOL;
END_VAR
LD
  RUNG div (* DIV into a DINT and an INT *)
    { dq := a / b; iq := DINT_TO_INT(a / 2) }

  RUNG cpt (* CPT into a DINT *)
    { dc := a / b + c }

  RUNG viatag (* the quotient into a DINT tag, then into a REAL *)
    { q := a / b; rq := q }

  RUNG real (* a REAL divided by an integer is a REAL division on both *)
    { rr := x / b }

  RUNG cmpi (* CMP against an integer *)
    GT(a / b, c) ( gi )

  RUNG cmpx (* the quotient's tag against a REAL *)
    GT(q, x) ( gx )
END_LD
END_PROGRAM
