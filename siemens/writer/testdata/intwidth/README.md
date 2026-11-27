# intwidth fixture (#1305 B1)

Integer arithmetic where nautilus computes at 64 bits, as the Siemens writer
writes it:

- A conversion of integer arithmetic: `TO_INT` wraps the 64-bit value as a store, `A_TO_B` wraps to A first, and `TO_LINT` and `TO_REAL` convert from LINT.
- SEL, MAX and LIMIT pass the 64-bit value to the store, which wraps.
- FOR loops:
  - TIA's FOR is used only where the loop provably ends on the CPU.
  - Every other FOR is the LINT counter loop (forloop.go):
    - a loop up to the type's last value;
    - an end below the type;
    - a loop down to the type's first value;
    - a variable end of the variable's own type;
    - a variable step (0 faults, as in nautilus);
    - plclab's `FOR j := 0 TO nb - 1`;
    - an end past INT's range, which faults the scan at 32768, as nautilus does (#1427).
  - After the loop the variable holds nautilus's exit value, wrapped to its type (#1427): the loops to the type's last value leave 32767 + 1 wrapped, -32768.

Main.st is also the B1 bench program: every result is a tag, read from the CPU and from naut run with the same inputs.

| Tag | Value |
|---|---|
| D1 | 2147483000 |
| D2 | 1000 |
| N | 32767 |
| M | -32768 |
| S | 1 |
| Z | 0 |
