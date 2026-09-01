# ladder-subroutines — FUNCTION_BLOCKs written as rungs

```
nautilus check examples/ladder-subroutines
nautilus test  examples/ladder-subroutines -v
nautilus run   examples/ladder-subroutines
```

`blocks.ld` has **no PROGRAM**, which makes it a project library — the
same rule that makes a PROGRAM-less `.st` file one. Its two
`FUNCTION_BLOCK`s have ladder bodies, and `main.ld` instantiates each of
them twice, once per pump:

```
RUNG lead
  P101Start p101:PumpSeq(Stop := P101Stop, Level := LevelPct,
                         StopLevel := StopLevel,
                         Run => P101Run, Warm => P101Warm)
```

The rung's power drives the block's first free BOOL input (`Start`) and
continues from its first BOOL output (`Run`); every other pin is bound by
name, and `=>` captures an output into a tag. Each instance keeps its own
seal-in and its own retained `TON` — the acceptance suite starts the two
pumps three seconds apart and asserts each `Warm` bit separately, which
is the whole point of an instance.

A ladder block is an ordinary IEC `FUNCTION_BLOCK` by the time the
compiler sees it, so ST and FBD programs in the same project call it the
same way:

```iecst
VAR p : PumpSeq; END_VAR
p(Start := Cmd, Level := LevelPct, StopLevel := 80.0);
PumpRun := p.Run;
```

See `docs/functions.md` → "Function blocks in ladder".
