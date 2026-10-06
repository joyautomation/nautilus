(* The Logix dialect: blocks with Allen-Bradley semantics, written once in
   nautilus so the runtime, the Logix writer and the L5X importer share the
   definition. A project opts in with `dialect: logix` in nautilus.yaml.

   TONR is a TON with a Reset pin — the Logix idiom `XIC(x)RES(t)` beside a
   `TON(t)`: while Reset is TRUE the accumulated time and Q are cleared;
   otherwise it times like a TON. The free-running pulse `XIC(t.DN)RES(t)`
   is `t:TONR(PT := T#1S, Reset := t.Q)`. The Logix writer emits it as a
   TON with a RES rung ahead of the timer's rung. *)
FUNCTION_BLOCK TONR
VAR_INPUT
    IN    : BOOL;
    PT    : TIME;
    Reset : BOOL;
END_VAR
VAR_OUTPUT
    Q  : BOOL;
    ET : TIME;
END_VAR
VAR
    t : TON;
END_VAR
t(IN := IN AND NOT Reset, PT := PT);
Q := t.Q AND NOT Reset;
ET := t.ET;
END_FUNCTION_BLOCK
