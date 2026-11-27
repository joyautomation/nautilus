# b2 fixture (#1305 B2)

The integer ABS, EN/ENO and ATAN2 constructs, as the Siemens writer writes them:

- **Integer ABS** at 64 bits, wrapped by the store, for the least INT, DINT and LINT values, a small literal, and the three least literals (folded).
- **EN/ENO** on LIMIT, a function call that is gated with a side-effecting argument (`Bump`, which counts its runs), and on a CTU with `CV =>`. ENO is the gate's branch.
- **ATAN2** through the generated `naut_ATAN2` (Go's math.Atan2):
  - every pair of the special values (±0, ±1, ±inf, NaN), made at run time, in LREAL;
  - a 12×12 REAL grid;
  - a quotient that overflows, and one that underflows.

Main.st is also the B2 bench program: the results are tags, compared between the CPU and naut run (the special values against Go's math.Atan2 bit for bit).
