# Testing

Control logic gets acceptance tests, with no Go and no toolchain. Tests
live in `*_test.yaml` beside `nautilus.yaml`, and `nautilus test` runs
them.

```sh
nautilus test           # run every *_test.yaml in the project
nautilus test -v        # with the virtual time each test covered
nautilus test -run re   # only tests whose name matches
nautilus test -json     # one NDJSON event per test, for editors and CI
```

- [Why virtual time](#why-virtual-time)
- [A test file](#a-test-file)
- [Steps and time](#steps-and-time)
- [`given` — setting the world](#given--setting-the-world)
- [Expectations](#expectations)
- [Freezing tasks](#freezing-tasks)
- [Reading a failure](#reading-a-failure)
- [In the editor](#in-the-editor)
- [In CI](#in-ci)
- [From a Go project](#from-a-go-project)
- [What these tests cannot do](#what-these-tests-cannot-do)

## Why virtual time

A PLC test that loops the scan a hundred times covers a few hundred
*microseconds* of process time. That is why control-logic tests, when they
exist at all, only ever assert direction — "the heater turns on when it's
cold" — and never a delay, a debounce, or a settling time. The interesting
properties are all time-dependent, and a wall clock puts them out of reach.

`nautilus test` runs the resource on a **virtual clock**. Both clocks a
program can observe follow it:

- the measured scan-to-scan `dt` bound to `dt-tag`, which every PI loop,
  integrator, and ramp integrates against;
- the millisecond base `TON` / `TOF` / `TP` count from, and every block
  built on them.

So `advance: 10s` runs ten seconds of process time, exactly, in well under
a millisecond. A ten-second on-delay is an ordinary thing to assert:

```yaml
  - name: low-temp alarm waits its full 10 s
    suspend: [sim]                    # freeze the plant; drive the value directly
    given: { TempC: 45.0 }
    steps:
      - advance: 9.5s
        expect: { TempLowAlm: false } # the TON has not elapsed
      - advance: 1s
        expect: { TempLowAlm: true }  # ... and now it has
```

Runs are deterministic. The scheduler lands the clock exactly on each tick
rather than near it, so `dt` is exactly the configured period and a loop's
trajectory is identical on every run and every machine.

## A test file

```yaml
# heated-tank_test.yaml
tolerance: 0.5     # default ± for `near` in this file
suspend: []        # tasks frozen in every test in this file

tests:
  - name: pump seals in below the start level
    given:  { LevelPct: 35.0 }
    scans:  1
    expect: { PumpRun: true }
```

**The fixture is `nautilus.yaml`.** Tasks, scan rates, tag roles, seeds,
units, and descriptions all come from the manifest that deploys, so a test
never restates them and can never drift away from what actually runs.
Retune a gain in the manifest and the tests retune with it.

A test is:

| key | meaning |
| --- | --- |
| `name` | required; the identity in output and `-run` patterns |
| `given` | initial conditions, applied **before the first scan** |
| `suspend` | tasks that don't scan in this test (overrides the file default) |
| `tolerance` | default `near` tolerance for this test |
| `steps` | a list of steps — or omit it and write one step's keys inline |

A test with no `steps:` **is** one step, so simple cases stay short.

## Steps and time

| key | meaning |
| --- | --- |
| `given` | writes applied before this step's first scan |
| `scans: N` | run until the main task has completed N more scans |
| `advance: 12s` | run every due tick up to `now + 12s`, then land exactly there |
| `until: 45s` | run up to 45 s, stopping as soon as `expect` holds |
| `hold: 5s` | with `until`: how long the expectation must *stay* true |
| `expect` | checked at the end of the step (with `until`, at each tick) |
| `always` | checked after **every** scan in the step — invariants, not endpoints |

Exactly one of `scans` / `advance` / `until` per step. Omit all three to
assert on the seeded state before anything runs.

`scans` is the honest unit for combinational logic — a latch, a
comparison, a clamp — where "one scan" is the whole question. `advance` is
the unit for everything time-dependent.

`until` + `hold` is what a settling test actually needs. A loop that merely
passes through its target on the way to an overshoot has not settled, and
a bare "assert after 45 s" cannot tell the difference:

```yaml
  - name: PI settles a setpoint step and holds it
    steps:
      - until: 300s
        expect: { TempC: { near: 65.0 } }
      - given: { TempSP: 72.0 }
        until: 45s
        hold: 5s
        expect:
          - ABS(TempC - TempSP) < 0.5
        always:
          Heater: { between: [0.0, 100.0] }   # the clamp never breaks
```

## `given` — setting the world

`given` writes tags, routed by the **role the manifest already declared**:

- a `role: input` tag goes to the driver's input image, and arrives in the
  store at the top of the next scan, exactly as the field would deliver it;
- everything else is written straight to the tag store.

You don't restate which is which. That matters at commissioning: when a
tag moves from `role: state` to `role: input` because a real driver has
taken over from the simulation, no test changes.

Writing an undeclared tag is an error, not a new tag.

**Tests never open a socket.** Whatever `driver:` says — including `eip` —
the test harness substitutes a stub, so a project bound to real hardware
is fully testable on a laptop with nothing on the network.

## Expectations

Per tag, either a value or a matcher:

| form | meaning |
| --- | --- |
| `PumpRun: true` | exact — BOOL, STRING, or a number |
| `{ near: 72.0, tol: 0.5 }` | within a tolerance (`tol` defaults to the file's `tolerance`) |
| `{ gt: }` `{ ge: }` `{ lt: }` `{ le: }` | numeric bounds |
| `{ between: [64.5, 65.5] }` | inclusive range |
| `{ eq: 0 }` | exact, spelled out |

Or an **ST expression**, compiled by the same compiler as the logic under
test — for anything a fixed matcher vocabulary can't say: expectations
computed from other tags, relationships between them, and reusable
predicates. Which one you wrote is unambiguous: a mapping is tag matchers,
a string is an expression, and a list may mix both.

```yaml
    expect:
      - PumpRun: true
      - ABS(TempC - TempSP) < 0.5        # relational — tracks the setpoint tag
      - NOT Overshot(TempC, TempSP, 2.0) # a FUNCTION from your library file
```

Reusable assertions are ordinary ST `FUNCTION`s returning `BOOL`, in the
project's library files — the same composition your logic already uses, so
they're callable from FBD and ladder too.

Program locals are addressable as `task.local` — `main.integral` — for the
retained state a program keeps but never publishes as a tag.

One assertion is implicit: **a scan that faults fails the test that ran
it**, with the error and the virtual timestamp. You never write that one.

## Freezing tasks

`suspend: [sim]` stops the named tasks from scanning for the duration of a
test. Freezing the task that simulates the plant is what turns a
closed-loop project into an open-loop test: drive the process value
directly and nothing fights you for it.

Everything declared runs by default. Naming what you freeze states the
intent — *this test drives the temperature, so the plant must not fight
it* — and it survives a fifth task being added to the project.

## Reading a failure

A failure says what broke, when in virtual time, and what the process was
actually doing — with the units and descriptions from your manifest:

```
  FAIL  PI settles a 65 to 72 C step within 20 s
        heated-tank_test.yaml:7 — step 2, t=3m50s
          ABS(TempC - TempSP) < 0.5   is false   (never held for 5s within 20s)

          TempC  °C  — Tank temperature
                 3m30s    3m35.7s    3m41.4s    3m47.1s      3m50s
               64.5001    66.1717     67.663    68.9935    69.6143
```

The trajectory is the point. Here the loop is fine and still converging —
the 20 s contract was too tight, which the numbers say and a bare
pass/fail wouldn't.

## In the editor

With the VS Code extension installed, a suite gets the treatment a program
gets.

The Test Explorer discovers every `*_test.yaml` in the workspace and runs
them individually, by file, or all at once; a failure reports its line and
its trajectory.

Expectations written as ST are compiled as you type, against the tags your
project actually has — what `nautilus.yaml` declares, unioned with what
the programs bind, so a tag with no `init:` is typed by the program that
writes it. A name that doesn't exist is a squiggle before anything runs:

```
expect:
  - ABS(TempX - TempSP) < 0.5     undeclared identifier "TempX"
```

Hover a tag for what the manifest says it is — its unit, its description,
and what its role means in the scan. Completion offers the project's tags,
ST's builtin functions, and the `FUNCTION`s declared in your own library
files, which makes the reusable-predicate story discoverable rather than
something you have to remember.

All of that comes from the CLI's language server, so it needs `nautilus`
on your `PATH`. The rest of the file — keys, durations, matcher shapes —
is checked against a JSON schema, which needs the YAML extension
(`redhat.vscode-yaml`); the ST parts don't.

## In CI

The scaffolded workflow gates on all three:

```yaml
      - run: nautilus check .
      - run: nautilus test .
      - run: nautilus build -o my-plant
```

`nautilus test` exits non-zero on any failure. `-json` emits one
line-delimited event per test for editors and CI tooling.

Test files never reach a deployed controller: `nautilus build` excludes
`*_test.yaml` from the binary's embedded project. They gate the deploy;
they don't ride along on it.

## From a Go project

An SDK project runs the same suites through the same harness, so there is
one test story rather than two:

```go
func TestAcceptance(t *testing.T) {
    fsys := os.DirFS(".")
    proj, err := project.Load(fsys)
    if err != nil {
        t.Fatal(err)
    }
    acceptance.Run(t, fsys, proj.Runtime)
}
```

Hand-written Go tests get the virtual clock too — `acceptance.NewRuntime`
returns a runtime and a scheduler with `Advance`, `Scans`, `AdvanceUntil`,
and `Suspend`, for anything the YAML can't express.

## What these tests cannot do

**They can't prove the absence of a race between tasks.** The harness
replays one deterministic interleaving — next task due, ties broken by
declaration order — and that determinism is exactly what makes a test
reproducible. Real scheduling gives every task its own ticker and lets the
OS decide; a passing suite says your logic is correct under one valid
interleaving, not under all of them. That is a deliberate trade.

They also don't test the physics. A simulated plant is a fixture, not a
subject — it exists so the control logic has something to control.

And they don't touch hardware. For driver conformance against a real
controller, see the EtherNet/IP guide's emulator.
