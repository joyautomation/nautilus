# Design: acceptance testing for manifest projects

Status: **built and running.** `nautilus test` executes `*_test.yaml` suites in virtual
time; `examples/heated-tank-nogo/heated-tank_test.yaml` is the worked example and passes.
§1–§2 are the original feasibility findings, kept because they are why any of this exists.
The format is deliberately small and meant to be tweaked in use, not frozen by this document.
Author: feasibility spike, 2026-08-05; design, 2026-08-05
Scope: gives a manifest project a way to assert on its control logic, and gives the runtime the deterministic clock that any such mechanism needs.

---

## 0. Why this exists

The product direction is that the **manifest form is nautilus**, and Go becomes the extension tier for custom field buses and richer simulation. Everything else already supports that: the manifest is close to a strict subset of `runtime.Options` in expressive power (multi-task, per-task scan rates, `dt-tag`, tag roles, units/descriptions, library composition, EtherNet/IP), and what is genuinely manifest-only is *lifecycle*, not capability — `nautilus run` / `nautilus build` need no toolchain, and the online-edit → `nautilus pull` → commit loop closes without a rebuild because the files are what the runtime loads.

Two things stand between the manifest tier and being the whole product: **custom drivers** and **tests**. Custom drivers are a legitimate SDK concern where Go belongs. Tests are not.

The stake is concrete. The pitch against vendor tooling is that PLC code has no tests and no pipeline that runs one. If the default nautilus project answers that with a compile check and a `git diff`, the argument is materially weaker than it should be. Today:

- `nautilus test` does not exist (`cmd/nautilus/main.go`, command switch).
- The scaffolded CI for a manifest project runs `nautilus check` + `nautilus build` and nothing else; the Go branch is the only one with `go test ./...` (`cmd/nautilus/templates/ci.yml.tmpl`).
- Acceptance testing exists only as `cmd/nautilus/templates/program_test.go.tmpl`, in the Go tier.

## 1. The blocking finding: the runtime has no virtual time

**This is the reason a test format alone would not solve the problem.** It is not specific to the manifest tier — the existing Go tests have the same hole.

There are two independent clocks, both wall-clock:

1. **Scan `dt`** — `runtime/runtime.go:358` `Scan()` computes
   `dt := t0.Sub(r.lastScan).Seconds()`, falling back to the configured target only on the first scan (`runtime.go:359-365`). `scanTask` does the same per task (`runtime.go:328-336`). This feeds `DtTag`, so it drives every PI loop, integrator, and the ST plant simulation.
2. **IEC timers** — `TON`/`TOF`/`TP` read `ctx.NowMs` (`lang/ir/builtins_fb.go:80,125,163`), supplied via the `Host` interface (`lang/ir/vm.go:10,202`) and implemented as `func (t *Tags) NowMs() int64 { return time.Now().UnixMilli() }` (`runtime/tags.go:44`).

Consequence: a test that loops `rt.Scan()` 100 times simulates a few hundred microseconds of process time, not 10 seconds. So none of the following can be asserted today, in either tier:

- the 10-second `TempLowAlm` delay in `examples/heated-tank-nogo`
- PI settling (measured: a `TempSP` 65 → 72 °C step settles in ~32 s, heater saturating at 100 % for ~26 s before backing off)
- any `TON`/`TOF`/`TP`, i.e. most real interlock logic

This is why `program_test.go.tmpl` only ever asserts *direction* ("heater should drive on cold error, got > 0") and never a delay, a timer, or a convergence. The tests were written to fit what is testable.

**Both clocks already have a seam**, which is the good news:

- `NowMs()` is already an interface method on `ir.Host`. Making the implementation swappable is small and touches no VM code.
- Scan `dt` is computed inline and needs a real (small) change to accept an injected step.

Get both and `advance: 12s` becomes deterministic *and* instant.

## 2. Second constraint: multi-task scheduling is non-deterministic

`Run` starts one goroutine per additional task, each with its own `time.Ticker`, serialized only by the per-scan lock (`runtime/runtime.go:286-311`). `heated-tank-nogo` has four tasks at 100 ms / 100 ms / 1 s / 200 ms.

A test harness therefore **must not use `Run`**. It needs its own virtual scheduler that replays tick order deterministically (next-due-time, ties broken by declared order). The primitives already exist: `Scan()` for the main task and `ScanTask(name)` (`runtime.go:315`), whose doc comment already says "for tests and custom schedulers."

Worth stating explicitly in the design: a deterministic interleaving is what makes tests reproducible, and it also means **tests cannot prove the absence of races between tasks**. That is a deliberate trade, not an oversight, and it is written down in §8.

---

## 3. The recommendation stands: declarative YAML, not a DSL

Agreed with the brief, for its stated reasons (tiny regular assertion surface; a DSL is a
grammar + parser + diagnostics + LSP, permanently; YAML diffs in git; it extends the
existing "the system is declared, not wired" frame).

Two arguments the brief did not make, both of which strengthen it:

- **A YAML schema is shippable tooling.** A JSON Schema for `*_test.yaml`, referenced from
  the VS Code extension's `yaml.schemas` contribution, buys completion, hover docs, and
  inline validation for free — for a DSL, all of that is code we write and maintain. The
  extension already carries four IEC languages; the fifth thing it should carry is a schema,
  not a parser.
- **Tests become machine-editable.** A structured file can be generated and rewritten —
  by `nautilus new`, by an "add a test for this rung" editor action, by an agent. A bespoke
  syntax makes every one of those a codegen problem.

The door stays open exactly where the brief left it: if sequencing tests ("force a fault at
step 3, assert it aborts") outgrow this, the answer is **test POUs in ST**, reusing the
compiler, LSP, and editors — not a new grammar.

## 4. The format

### 4.1 Where tests live

**Separate `*_test.yaml` files**, discovered by walking the project directory (skipping
dot-directories). Not a `tests:` key in `nautilus.yaml`.

Rationale: `nautilus.yaml` is the *deployment artifact* — it is embedded verbatim into the
binary by `nautilus build`, it is what `nautilus pull` round-trips against, and it is the
thing an operator reads to understand what is deployed. Tests are dev-time, they get long,
and they change on a different cadence. Keeping them out also means `nautilus build` ships
no test data (the build step will exclude `*_test.yaml` from the embedded archive).

The `_test` suffix is deliberate: it mirrors Go, and it makes "is this file a test?" a rule
both the CLI and a human can apply without opening the file.

### 4.2 A file

```yaml
# heated-tank_test.yaml
tolerance: 0.5           # default tolerance for `near` in this file (optional)
suspend: []              # tasks frozen in every test in this file (optional)

tests:
  - name: pump seals in below the start level
    given:  { LevelPct: 35.0 }
    scans:  1
    expect: { PumpRun: true }
```

A test is:

| key | meaning |
|---|---|
| `name` | required; the identity in output and `-run` patterns |
| `given` | initial conditions, applied **before the first scan** |
| `suspend` | tasks that do not scan in this test (overrides the file default) |
| `tolerance` | default `near` tolerance for this test |
| `steps` | a list of steps; **or** omit it and write one step's keys inline |

A step is:

| key | meaning |
|---|---|
| `given` | writes applied before this step's first scan |
| `scans: N` | run until the **main** task has completed N more scans |
| `advance: 12s` | run every due tick up to `now + 12s`, then land the clock exactly there |
| `until: 45s` | run up to 45 s, stopping as soon as `expect` holds; fail if it never does |
| `hold: 5s` | with `until`: the expectation must stay true this long to count as held |
| `expect` | checked once at the end of the step (with `until`, at every tick until it holds) |
| `always` | checked after **every** scan in the step — invariants, not endpoints |

Exactly one of `scans` / `advance` / `until` per step. A step with no time key runs zero
scans (useful to assert on the seeded state before anything runs).

### 4.3 `given` routes by declared role — one key, two seams

This is the one place I argue with the brief. §4.3 of the brief says the format must not
blur driver inputs and tag writes because they are different seams. They are — but the
manifest **already declares which is which**, per tag, by role. Making the test restate it
duplicates knowledge that has exactly one source of truth, and it breaks on the most common
refactor there is: a tag flips `role: state` → `role: input` at commissioning when the sim
task is deleted and a real driver takes over (`examples/heated-tank-nogo/nautilus.yaml`
says this in a comment). Under role-routing that refactor changes no test. Under an
explicit `inputs:` / `tags:` split, it silently changes what every test means.

So: `given: { TempC: 45.0 }` writes the tag store when `TempC` is `state`/`setpoint`/`output`,
and writes the **driver input image** when it is `role: input` — where it will be copied into
the store at the top of the next main scan, exactly as the field would. An unknown tag name
is an error, not a new tag.

**The driver is always a stub.** `nautilus test` replaces whatever the manifest configures
with an `io.Memory` driver, regardless of `driver.type`. Tests never open a socket, never
touch hardware, and a project with `driver: eip` is fully testable on a laptop with no
controller on the network. `role: input` tags are fed from `given`; loopback behavior is
identical to `nautilus run` with the default memory driver, because it *is* that driver.

### 4.4 Assertions and REAL tolerance

Per tag, the expectation is either a bare scalar or a matcher object:

| form | applies to |
|---|---|
| `PumpRun: true`, `Status: 'tank 65 degC — ok'` | BOOL, STRING — exact |
| `{ near: 72.0, tol: 0.5 }` | REAL — `tol` defaults to the test/file `tolerance` |
| `{ gt: 0 }` `{ ge: }` `{ lt: }` `{ le: }` | numeric bounds |
| `{ between: [64.5, 65.5] }` | numeric range, inclusive |
| `{ eq: 0 }` | exact numeric — opt-in |

**Shipped behavior: a bare number is exact.** The strict version — a bare number being a
load-time error, forcing every numeric comparison to declare a tolerance — was the original
proposal and was softened during the design discussion. With a schema and completion doing
the teaching, and with runs bit-reproducible under virtual time (so an exact comparison is
deterministic, merely brittle), the hard error was overkill.

**Still to do:** the nudge that replaces it — a schema warning when a bare number is compared
against a tag whose manifest `init` is a float, suggesting `{near: …, tol: …}`. The manifest
already carries the type information; nothing consumes it yet.

Program locals are addressable as `task.local` — `main.integral`, `reports.hist` — through
`TaskProgram(name).Locals()`. Unqualified names are always tags, so the namespace stays
unambiguous.

**Implicit assertion:** any scan that returns a logic error fails the test immediately, with
the error and the virtual timestamp. You never have to ask for that.

### 4.5 The sim task: both, by exclusion

`suspend: [sim]` freezes named tasks for the duration of a test. The default is that
**everything declared runs** — the project as configured, closed loop, least surprising.

Exclusion rather than inclusion (`tasks: [main, interlocks]`) because a test that names what
it freezes states its *intent* ("this test drives temperature directly, so the plant must not
fight it"), and because adding a fifth task to the project should not silently drop it out of
every existing test.

### 4.6 Output

Default: human-readable, `go test`-shaped, with **virtual** elapsed time — the number that
means something.

```
heated-tank_test.yaml
  ok    pump seals in below the start level         1 scan,  0.100s
  ok    low-temp alarm waits its full 10 s        106 scans, 10.500s
  FAIL  PI settles after a 65 → 72 °C step
        step 2 (until 45s, hold 5s), t=+105.000s
          TempC = 70.812, want 72.000 ± 0.500
          last satisfied at t=+103.100s, held 1.900s

3 tests, 2 passed, 1 failed
```

Exit 1 on any failure. `--json` emits NDJSON events (`run`/`pass`/`fail` per test, with file,
line, tag, want, got, virtual time) for the VS Code extension's test explorer. `-run <regex>`
and `-v` behave like `go test`. Not TAP: it loses the structure the extension wants and reads
worse than this in a CI log.

Deliberately *not* in v1, but cheap later: `--trace TempC,Heater` printing the tag's value at
every tick of a failing step. That is the thing you actually want when a PI test fails, and
the harness already visits every tick.

### 4.7 The Go tier converges on the same harness

The harness ships as an exported package, `github.com/joyautomation/nautilus/acceptance`:

```go
func TestAcceptance(t *testing.T) { acceptance.Run(t, ".") }              // manifest project
func TestAcceptance(t *testing.T) { acceptance.RunOptions(t, opts, ".") } // Go project
```

`nautilus test` is a thin CLI over the same package. One test story, two entry points; a Go
project keeps hand-written Go tests for anything the YAML cannot say, and gets virtual time
in them too (§5 makes the clock an ordinary `runtime.Options` field).

## 5. Virtual time: what it reaches, and what it does not

A `Clock` on `runtime.Options`, defaulting to nil = wall clock:

```go
type Clock interface{ Now() time.Time }
```

It reaches exactly the two things a *program* can observe:

1. **Scan `dt`**, main and per-task. `Scan`/`scanTask` keep computing measured
   scan-to-scan seconds — the semantics do not change, the clock underneath does. With the
   scheduler landing the clock exactly on each due instant, `dt` is exactly the configured
   period, and the existing first-scan fallback still applies.
2. **`ir.Host.NowMs`**, via `Tags` — so `TON`/`TOF`/`TP` and every user FB built on them
   follow virtual time. No VM change; `NowMs` is already an interface method.

It reaches nothing else. Specifically **not** Sparkplug timestamps, the SSE `ts` field, or
the scan-diagnostic phase timings (`ReadMs`/`ExecUs`/`WriteMs`/`LastMs`), which keep
`time.Now()` because they measure real execution cost and that is real even in a test. This
is safe by construction rather than by discipline: the clock is a field on one `Runtime`, not
a global, and `nautilus test` starts no server, no Sparkplug node, and no real driver.

**Hot path:** the default path takes no extra `time.Now()`. `Scan` reads the wall clock once
as it does today and uses that value for `dt` as well, substituting the injected clock only
when one is present:

```go
t0 := time.Now()          // phase timings — always wall
now := t0                 // dt basis
if r.clock != nil { now = r.clock.Now() }
```

Scheduling rules, all deterministic:

- Virtual time starts at a fixed epoch (2000-01-01T00:00:00Z), so `NowMs` is reproducible.
  **The epoch must also be non-zero**, which turned out to be load-bearing rather than
  cosmetic: `TON`/`TOF`/`TP` store "`NowMs` at the rising edge" in an internal slot and
  treat **zero as idle** (`lang/ir/builtins_fb.go`). Start virtual time at 0 ms and a timer
  armed on the first scan re-arms itself forever — the delay under test could never elapse.
- Each task first comes due one interval in, not at t=0, matching what `Run`'s tickers
  actually do. So at t=0 the resource has not scanned and a test can assert on the seeded
  state before anything runs.
- Thereafter each task is due at multiples of its period; ties run main first, then
  declaration order. The clock is set to the due instant *before* the batch runs. Ties are
  the common case, not the edge case — the example's 100 ms control task and 100 ms sim task
  coincide on every tick.
- A suspended task keeps its phase; resuming re-phases it to the next tick at or after now,
  so virtual time never runs backwards and a resumed task does not replay what it slept
  through.
- `advance: d` runs every tick due at or before `now+d`, then sets the clock exactly to
  `now+d`. A partial tick does not execute, so `advance` is exact, not rounded.
- Suspended tasks neither scan nor hold the clock back.

This dissolves the brief's §4.2 tension. Fixed-step and wall-clock-shaped are not a
trade-off once time is virtual: `advance: 12s` *is* 120 ticks of a 100 ms task, exactly. Both
spellings ship, because `scans: 1` is the honest unit for combinational logic and `advance`
is the honest unit for everything time-dependent.

## 6. The acceptance bar, written out

Against `examples/heated-tank-nogo`, both tests from §9 of the brief, deterministic and
instant:

```yaml
# examples/heated-tank-nogo/heated-tank_test.yaml
tolerance: 0.5

tests:
  - name: pump seals in below the start level and drops out above it
    suspend: [sim]                     # level is driven by the test, not the plant
    given:  { LevelPct: 35.0 }
    steps:
      - scans: 1
        expect: { PumpRun: true }
      - given:  { LevelPct: 60.0 }     # between the bands — seal-in holds
        scans:  1
        expect: { PumpRun: true }
      - given:  { LevelPct: 80.0 }
        scans:  1
        expect: { PumpRun: false }

  - name: low-temp alarm waits its full 10 s
    suspend: [sim]                     # freeze the plant; drive TempC directly
    given:  { TempC: 45.0 }            # below the 62 °C cold threshold
    steps:
      - advance: 9.5s
        expect: { TempLowAlm: false }  # TON has not elapsed
      - advance: 1s
        expect: { TempLowAlm: true }   # ... and now it has
      - given:  { TempC: 70.0 }
        scans:  1
        expect: { TempLowAlm: false }  # TON drops out with no off-delay

  - name: PI settles a 65 → 72 °C setpoint step within 45 s
    # closed loop: sim.st runs, so this exercises control against the plant
    steps:
      - advance: 120s                  # reach steady state at the 65 °C setpoint
        expect: { TempC: { near: 65.0, tol: 0.5 } }
      - given:  { TempSP: 72.0 }
        until:  45s                    # ~32 s measured; 45 s is the contract
        hold:   5s                     # settled, not merely passing through
        expect: { TempC: { near: 72.0, tol: 0.5 } }
        always: { Heater: { le: 100.0 } }   # anti-windup clamp never breached

  - name: high-temp interlock sounds the horn until acked
    suspend: [sim]
    given:  { TempC: 95.0 }
    steps:
      - advance: 4s
        expect: { HiTempAlm: false }   # interlocks.ld runs a 5 s TON at 200 ms
      - advance: 1.5s
        expect: { HiTempAlm: true, Horn: true }
      - given:  { HornAck: true }
        scans:  1
        expect: { Horn: false, HiTempAlm: true }
      - given:  { TempC: 60.0 }        # both alarms clear → ack self-releases
        advance: 1s
        expect: { HiTempAlm: false, HornAck: false }
```

The third test is the one that decides the design: it needs virtual `dt` (the PI integral and
the plant integration), a closed loop across two tasks at two rates, a tolerance, and an
eventually-with-hold assertion. If it passes deterministically in milliseconds, the hole in
§1 is closed.

**Result: it does.** `acceptance/heated_tank_test.go` is the Go form of these tests against
the real example, and the whole file runs in ~40 ms of wall time. The measurements in §1
reproduce exactly — the heater saturates at 100 % for ~26 s and the step settles **32.4 s**
after it, then holds. Two runs of the same trajectory are bit-identical, and `dt` is exactly
`0.1` and `1.0` for the 100 ms and 1 s tasks rather than merely close.

One thing the test found immediately, which is the argument for having it: the loop's **cold
start is far slower than its step response** — 3 m 30 s to first reach 65 °C, against 32 s
for the 65 → 72 step. `Ki` is 0.15, so from a cold start the integral has to climb from zero
to the ~75 % the plant needs to hold setpoint, whereas after the step it starts most of the
way there. That is a real property of the tuning, it was invisible before, and nobody would
have gone looking for it at wall-clock speed.

## 7. What ships

1. ✅ **Done.** `runtime`: `Options.Clock`, threaded into `Scan`, `scanTask`, and
   `Tags.NowMs`; a `Schedule()` accessor returning each task's name and period (main
   included) so a scheduler can be written outside the package.
2. ✅ **Done.** `acceptance`: the virtual clock and deterministic scheduler (`NewRuntime`,
   `Advance`, `Scans`, `AdvanceUntil`, `Suspend`/`Resume`, `LogicErrors`), the suite loader,
   the matcher evaluator, ST-expression predicates, failure traces, the reporters (text and
   NDJSON), and `Run(tb, fsys, opts)` for the Go tier.
3. ✅ **Done.** `cmd/nautilus`: `nautilus test [dir] [-run re] [-v] [-json]`, which compiles
   first (a compile error is a suite failure carrying the diagnostic); `nautilus build`
   excludes `*_test.yaml` from the embedded archive.
4. ✅ **Done.** Scaffolding: every manifest template writes a `<name>_test.yaml`, the
   manifest CI template runs `nautilus test`, and `examples/heated-tank-nogo` carries a worked
   suite.
5. ✅ **Done.** Editor integration, in three layers:
   - **JSON Schema** (`tools/vscode-iec/schemas/nautilus-test.schema.json`) over the keys,
     with a Go guard (`acceptance/schema_test.go`) proving it accepts and rejects exactly what
     the loader does — it originally accepted three shapes the loader refused.
   - **Test Explorer** over `nautilus test -list` / `-json`, one CLI invocation per project.
   - **ST inside the YAML.** An injection grammar highlights expectation expressions, and
     `nautilus lsp` treats a `*_test.yaml` as a document of its own: it finds the expression
     regions with the same rule `Expect.UnmarshalYAML` uses, compiles each through
     `acceptance.CheckExpr` — the runner's own wrapper — and publishes diagnostics, hover, and
     completion against them. The tags in scope are `ExternalsOf`: what the store holds
     unioned with what the programs *declare*, which is the only way an unseeded tag like
     `Heater` or a `dt-tag` like `ScanDtS` is typed at all. That union needed
     `ir.Program.Globals`: globals have no slot, so `Slots` could never answer which tags a
     program binds.

Also still to do, in rough priority order:

- **A recorder**: capture a window of a live run off the SSE stream and emit a `*_test.yaml`.
  This is the DX argument that only works because tests are data, and it turns commissioning
  into a regression suite.
- **Locals in expressions.** `expect` resolves `task.local` (e.g. `main.integral`), but an
  ST expression cannot reference one — the generated `VAR_EXTERNAL` block only covers tags.
- **Column-precise squiggles.** The compiler positions errors at statement granularity and an
  expression is exactly one statement, so an editor squiggle spans the whole expression. Same
  behaviour a `.st` file gets; finer positions would need the lowerer to carry sub-expression
  spans.

## 8. Non-goals and known limits

- **Not a race detector.** The deterministic interleaving is what makes tests reproducible;
  the cost is that a test can never prove the absence of a race between tasks. Real
  scheduling is non-deterministic by §2 and stays that way at run time. Deliberate trade.
- **Not a replacement for the Go acceptance tests.** They keep working, and they gain virtual
  time for free.
- **Not testing the physics.** `sim.st` and `plant.go` are fixtures, not subjects.
- **No hardware-in-the-loop or driver conformance testing.** The driver is always stubbed.
- **No fault injection** in v1 (driver read failures, logic errors on demand). The seam
  exists — the stub driver — so it is additive later.

## 9. Reference points

| What | Where |
|---|---|
| Reference test shape | `cmd/nautilus/templates/program_test.go.tmpl` |
| Scan loop + `dt` | `runtime/runtime.go:358` (`Scan`), `:328` (`scanTask`) |
| Task scheduling | `runtime/runtime.go:286-311` (`Run`), `:315` (`ScanTask`) |
| Timer clock | `runtime/tags.go:44`, `lang/ir/vm.go:10,202`, `lang/ir/builtins_fb.go` |
| Tag roles → I/O lists | `runtime/tagdef.go` (`expandTags`) |
| Manifest schema + load | `internal/project/project.go` (`Project`, `Load`) |
| Stub driver | `io/io.go` (`Memory`) |
| Scaffolded CI | `cmd/nautilus/templates/ci.yml.tmpl` |
| CLI command switch | `cmd/nautilus/main.go` |
| Expression wrapper (shared) | `acceptance/expect.go` (`exprSource`, `ExternalsOf`, `CheckExpr`) |
| Suites in the language server | `internal/lsp/testdoc.go` |
| Tags a program binds | `lang/ir/program.go` (`Program.Globals`), `runtime/runtime.go` (`Runtime.Globals`) |
| Editor contributions | `tools/vscode-iec/package.json`, `syntaxes/nautilus-test.injection.json` |
