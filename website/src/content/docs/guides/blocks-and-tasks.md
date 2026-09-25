---
title: Function blocks, libraries, and tasks
description: Structuring logic beyond one file — FUNCTION_BLOCKs and FUNCTIONs in library files, and multiple programs scheduled as tasks.
---

Once a program no longer fits in one file, IEC 61131-3's unit of reuse is the
**`FUNCTION_BLOCK`** (stateful — each instance keeps its own timers,
integrals, latches) and the **`FUNCTION`** (stateless). nautilus sticks to
the standard here on purpose: there is no vendor-style "call another
program" — a program is a scheduling unit, a function block is a reuse
unit, and reaching for reuse means writing a block.

Blocks live in **library files** — `.st` files holding only `TYPE`,
`FUNCTION`, and `FUNCTION_BLOCK` declarations — and compose ahead of the
program:

```go
//go:embed blocks.st
var blocks string

rt, _ := runtime.New(runtime.Options{
    Program:   program,            // .st or .fbd
    Libraries: []string{blocks},   // TYPEs, FUNCTIONs, FUNCTION_BLOCKs
    ...
})
```

```iecst
(* blocks.st *)
FUNCTION_BLOCK PI
VAR_INPUT  SP : REAL; PV : REAL; KP : REAL; KI : REAL; DT : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
VAR integral : REAL; err : REAL; END_VAR
err := SP - PV;
integral := LIMIT(0.0, integral + KI * err * DT, 100.0);
OUT := LIMIT(0.0, KP * err + integral, 100.0);
END_FUNCTION_BLOCK
```

```iecst
(* program.st — one instance per control loop *)
VAR tic : PI; END_VAR
tic(SP := TempSP, PV := TempC, KP := Kp, KI := Ki, DT := ScanDtS);
Heater := tic.OUT;
```

The pieces that make this first-class rather than a convention:

- **Callable from any IEC language.** The same `PI` block instantiates
  from an FBD diagram (`tic : PI(SP := TempSP, ...)`) exactly like a
  built-in TON — author blocks once, use them from whichever language
  fits the logic. Blocks can be authored in ladder or FBD as well: a
  `.ld` or `.fbd` file holding `FUNCTION_BLOCK`s is a library, the same
  as a `.st` one (see [Ladder](/languages/ladder/)).
- **The tooling composes the same way.** The VS Code extension, the LSP,
  `naut check`, `naut test`, and `naut pull` all treat the project's
  library files (the root's and `lib/`'s, [below](#where-library-files-live-the-root-and-lib))
  as in-scope for the program, byte-identically to `Libraries` — so
  online edits round-trip losslessly and CI sees what the runtime sees.
- **Instance state is retained.** A block's `VAR` section persists
  across scans, and PLC-style online edits carry it across program swaps
  by name and type — a `PI` keeps its integral through a live logic
  change, like a real controller.

`naut new` scaffolds this shape: the PI controller ships in
`blocks.st`, instantiated from `program.st`.

## Where library files live: the root and `lib/`

In a manifest project (`nautilus.yaml`), a library file is any `.st`, `.ld`
or `.fbd` file with **no `PROGRAM`** that sits either

- in the **project root**, beside `nautilus.yaml` and the program files, or
- anywhere under **`lib/`**, at any depth.

Once a project has more than a couple of blocks, `lib/` keeps them out of
the root, where the task programs are:

```
lift-station/
├── nautilus.yaml
├── sequence.sfc          # tasks: programs stay in the root
├── permissives.ld
├── sim.st
└── lib/
    ├── pump.st           # FUNCTION_BLOCKs and TYPEs
    ├── physics.st
    └── motor.ld          # a ladder block library
```

Folders inside `lib/` are fine too (`lib/physics/tank.st`).

Every library, root or `lib/`, composes into one prelude ahead of every
task, so any program can use any block and a block in `lib/` can use a
`TYPE` from the root (or from elsewhere in `lib/`). The order is fixed: the
`.st` libraries first, then the `.ld`/`.fbd` ones transpiled, each group
sorted by project-relative path (so `lib/physics.st`, `lib/pump.st` and a
root `units.st`, then `lib/motor.ld`). Order never decides whether a name resolves. It only decides
which file a duplicate-name error points at, and declaring a block in both
the root and `lib/` is the same error as declaring it in two root files.

Two rules keep this predictable:

- **`lib/` holds libraries only.** A file under `lib/` that declares a
  `PROGRAM` is an error that names it (`lib/extra.st declares a PROGRAM,
  but lib/ holds libraries only — programs belong in the root and in
  tasks:`). A program is a scheduling unit, so it lives in the root and is
  named by a task.
- **No other folder composes.** `hmi/`, `tags/`, `deploy/`, `node_modules/`
  and any other subdirectory are ignored, however many `.st` files they
  hold, so adding `lib/` changes nothing for a project that doesn't have
  one.

`naut build` ships `lib/` inside the binary with the rest of the project,
and error messages name library files by their project path (`lib/motor.ld`),
so a bad library is easy to find.

## More than one program: tasks

The spec's answer to "many programs" is the **resource/task model**:
several programs scheduled at their own rates against one shared tag store,
with no calling between them. `Options.Tasks` is exactly that:

```go
rt, _ := runtime.New(runtime.Options{
    Program: fastLogic,           // the MAIN task: owns field I/O
    Scan:    10 * time.Millisecond,
    Tasks: []runtime.Task{
        {Name: "temperature", Program: pidLoops, Scan: 250 * time.Millisecond, DtTag: "PidDtS"},
        {Name: "totals", Program: totalizers, Scan: time.Second, DtTag: "TotDtS"},
    },
})
```

Scans never overlap — tasks serialize on one lock, so every scan sees a
consistent tag snapshot. The main task reads inputs and writes outputs;
additional tasks compute against the store at their own pace, each with
its own measured-`dt` tag and its own health in `Stats().Tasks` (rendered
in the built-in dashboard and the HMI kit's `ScanDiagnostics`).

The full language reference — evaluation semantics and every built-in — is
in the [language reference](/reference/functions/).
