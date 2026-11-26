# arrays fixture (#1299)

Arrays as the Siemens writer writes them (arrays.go): bounds folded from
a block constant (N) and a project constant (STAGES : INT := 2 + 1), a
two-dimensional array with a negative lower bound, arrays of a PLC data
type, of an enumeration (started at its initial member), of String, an
array member in a STRUCT, an array tag with a start value, and an
array input of a FUNCTION.

The index checks: none for a literal inside the bounds or a FOR variable
whose constant range is inside them; a checked temp for the rest, in
nautilus's order of evaluation (an FB's inputs one by one, a FUNCTION
that can fault hoisted with its flag checked, an ELSIF's and a WHILE's
condition inside the branch and the loop); the fault propagated after a
call of a block that can fault, before its outputs are stored; the
faults DB and the OB's bookkeeping.

golden/ compiled 0 errors / 0 warnings on TIA V21 (tia1, 2026-11-26).
