# partial fixture (#1305 B3, and #1429)

Partial access as the Siemens writer writes it, as TIA's slice access (LSB-first, as nautilus numbers parts):

- reads of every byte, word and double word of a DINT;
- a part of a part, flattened (`Dx.%W1.%B0` is `Dx.%B2`, `Dx.%B1.%X3` is `Dx.%X11`);
- `.%L0` of a LINT, as its bit pattern;
- writes: a byte into the middle of a DINT, the sign byte of -1, a LINT's high double word, a WORD's high byte;
- `BYTE_TO_SINT` of a top byte;
- one shared DWORD whose byte 0 the main task writes and byte 1 the slow task writes, each flagging all 8 bits of its byte.

It also writes a #1429 long LREAL literal in each place one is written (a store, a binary, a call argument, ATAN2, a VAR_TEMP initializer, a negative), reading their bits back into LWORD tags.

Main.st and slow.st are also the B3 bench program (main 50 ms, slow 200 ms).

| Tag | Value |
|---|---|
| Dx | 16#01020304 |
| Neg | -1 |
| Top | 16#80000000 |
| Lx | 16#0102030405060708, set by the program |
| Wx | 16#1234 |
