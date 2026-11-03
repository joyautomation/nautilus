---
title: Your first program
description: Build one small program end to end — a sump pump with a level input, start and stop setpoints, a minimum-run timer and a start counter — declared, written, tested in virtual time, driven live with forces, and built.
sidebar:
  order: 0
---

[Getting started](/getting-started/) ends with a scaffold. This page takes
the next step: one small, real program, built end to end in about twenty
minutes. It is a single-pump sump (a lift station's wet well):

- the pump **starts** when the level reaches `StartLevel` (60 %) and
  **stops** when it falls to `StopLevel` (20 %);
- once started it runs for at least `MinRun` (30 s), so a quick pump-down
  does not short-cycle the motor — a `TON`;
- every start is counted — an `R_TRIG` feeding a `CTU`.

You will declare the tags, write the Structured Text, write acceptance
tests that pin the 30 s down in virtual time, run it and drive it over HTTP, and
build the controller binary.

You need `naut` on your `PATH` ([Getting started](/getting-started/), step
1). Steps 5 and 6 also use `curl` and `jq`.

## 1. Scaffold

```sh
naut new sump --template minimal --no-input
cd sump
```

```text
  created sump/ — a manifest project, no Go required

  next steps:
    cd sump
    naut run     # scan loop + dashboard + tag API on http://localhost:8080
    naut test    # acceptance tests, in virtual time
    naut build   # emit ./sump — a self-contained controller binary

  open the folder in VS Code with the "nautilus IEC 61131-3" extension for
  diagnostics, the graphical editors, and live values in program.st.
```

(`--no-input` skips the interactive form, which `naut new` opens whenever
it runs in a terminal.) You will replace the three files that matter:
`nautilus.yaml`, `program.st` and `sump_test.yaml`.

## 2. Declare the tags

Replace `nautilus.yaml` with:

```yaml
name: sump

server:
  addr: "localhost:8080"

tasks:
  - program: program.st
    scan: 100ms

tags:
  - { name: LevelPct,   role: input,    type: REAL, init: 0.0,   unit: "%", desc: Sump level }
  - { name: StartLevel, role: setpoint, type: REAL, init: 60.0,  unit: "%", desc: Pump starts at or above }
  - { name: StopLevel,  role: setpoint, type: REAL, init: 20.0,  unit: "%", desc: Pump stops at or below }
  - { name: MinRun,     role: setpoint, type: TIME, init: T#30s, desc: Shortest run once started }
  - { name: PumpRun,    role: output,   type: BOOL, desc: Pump run command }
  - { name: PumpStarts, role: output,   type: INT,  desc: Starts since power-up }

driver:
  type: memory
```

The role says who writes a tag:

| role | written by | here |
| --- | --- | --- |
| `input` | the field driver, before every scan | `LevelPct`, the level transmitter |
| `setpoint` | an operator (HMI or `POST /api/tags`); exists from scan one with its `init:` | the two levels and `MinRun` |
| `output` | the program; handed to the driver after every scan | `PumpRun`, `PumpStarts` |

Every program sees these tags without declaring them. `driver: memory` is a
loopback with no hardware behind it, so nothing will move `LevelPct` until
you do — in a test with `given:`, on a running controller with a force.
`unit:` and `desc:` show up in the dashboard and in test failures. More on
roles, and the one rule that bites (a tag is read before anything wrote
it), in [The tag model](/guides/tag-model/).

## 3. Write the logic

Replace `program.st` with:

```st
PROGRAM Sump
VAR
    run      : BOOL;    (* the pump's state, kept across scans *)
    minTimer : TON;     (* how long the pump has been running, up to MinRun *)
    started  : R_TRIG;  (* TRUE for one scan when the pump starts *)
    starts   : CTU;     (* counts those scans *)
END_VAR

IF LevelPct >= StartLevel THEN
    run := TRUE;
ELSIF LevelPct <= StopLevel AND minTimer.Q THEN
    run := FALSE;
END_IF;

minTimer(IN := run, PT := MinRun);
started(CLK := run);
starts(CU := started.Q, R := FALSE, PV := 32767);

PumpRun := run;
PumpStarts := starts.CV;
END_PROGRAM
```

- `run` is a local, so it keeps its value from one scan to the next: between
  the two levels the pump stays as it was.
- `minTimer` is an on-delay. Its `Q` turns TRUE once `run` has been TRUE for
  `MinRun`, and only then may the stop level stop the pump.
- `started` is TRUE for exactly one scan when `run` goes TRUE, and `starts`
  counts it. (`CTU` counts rising edges of `CU` on its own, so `CU := run`
  would count the same; the `R_TRIG` makes the start a pulse you can name
  and reuse.)
- The logic works on `run` and copies it to `PumpRun` at the end, so it
  never reads an output before writing it.

Check it compiles against the manifest:

```sh
naut check .
```

```text
naut check: 1 file(s), 0 with errors
```

Every block and operator is in the
[language reference](/reference/functions/); the ST syntax is in
[Structured Text](/languages/structured-text/).

## 4. Write the acceptance tests

Replace `sump_test.yaml` with:

```yaml
tests:
  - name: pump starts at the start level
    given: { LevelPct: 65.0 }
    scans: 1
    expect: { PumpRun: true, PumpStarts: 1 }

  - name: a quick pump-down still runs the full MinRun
    steps:
      - given: { LevelPct: 65.0 }
        scans: 1
        expect: { PumpRun: true }
      - given: { LevelPct: 15.0 }       # below StopLevel almost at once
        advance: 29.5s
        expect: { PumpRun: true }       # MinRun (T#30s) holds it on
      - advance: 1s
        expect: { PumpRun: false }      # ... and now it may stop

  - name: MinRun is a setpoint, so a longer one needs no program change
    given: { MinRun: T#2m }
    steps:
      - given: { LevelPct: 65.0 }
        scans: 1
      - given: { LevelPct: 15.0 }
        advance: 119.5s
        expect: { PumpRun: true, MinRun: T#2m }
      - advance: 1s
        expect: { PumpRun: false }

  - name: between the levels the pump keeps its state; each start counts once
    steps:
      - given: { LevelPct: 65.0 }
        advance: 31s
        expect: { PumpRun: true, PumpStarts: 1 }   # one start, not one per scan
      - given: { LevelPct: 40.0 }       # pumping down: keeps running
        advance: 10s
        expect: { PumpRun: true }
      - given: { LevelPct: 15.0 }
        scans: 1
        expect: { PumpRun: false }
      - given: { LevelPct: 40.0 }       # filling up: stays off
        advance: 10s
        expect: { PumpRun: false }
      - given: { LevelPct: 65.0 }
        scans: 1
        expect: { PumpRun: true, PumpStarts: 2 }
```

How to read it:

- The fixture is `nautilus.yaml`: every test starts from the manifest's
  tags, `init:` values and 100 ms scan. Nothing is restated.
- `given:` sets tags before the step runs. A `role: input` tag arrives
  through the driver at the top of the next scan, as the field would deliver
  it; a setpoint is written directly. A `TIME` tag takes an IEC literal,
  `T#2m`, in `given:` and `expect:` alike.
- `scans: 1` runs one scan. `advance: 29.5s` runs 29.5 s of **virtual**
  time: the clock the `TON` counts on moves exactly that far, in well under
  a millisecond of real time. That is what lets a test say "still on
  29.5 s after the level dropped, off a second later" and mean it.
- `expect:` is checked at the end of the step.

Run them:

```sh
naut test
```

```text
sump_test.yaml
  ok    pump starts at the start level
  ok    a quick pump-down still runs the full MinRun
  ok    MinRun is a setpoint, so a longer one needs no program change
  ok    between the levels the pump keeps its state; each start counts once

4 tests, 4 passed, 0 failed
```

### Break it on purpose

A test is worth what it catches. In `program.st`, delete ` AND minTimer.Q`
from the `ELSIF` line, so the stop level stops the pump at once, and run
`naut test` again:

```text
sump_test.yaml
  ok    pump starts at the start level
  FAIL  a quick pump-down still runs the full MinRun
        sump_test.yaml:14 — step 2 (line 12), t=29.6s
          PumpRun = false, want true   (expectation failed)

          PumpRun  — Pump run command
                  0.1s       4.3s       8.5s      12.7s      16.9s      21.1s      25.3s      29.6s
                  true      false      false      false      false      false      false      false
  FAIL  MinRun is a setpoint, so a longer one needs no program change
  ...

4 tests, 2 passed, 2 failed
```

The failure names the line of the expectation, the virtual time, and what
`PumpRun` actually did: on at the first scan, off by the next sample. Put
` AND minTimer.Q` back and the suite passes again. `naut test` exits
non-zero on a failure, which is what the scaffolded CI gates on. Every
step key, matcher and the `force:` step are in
[Testing](/reference/testing/).

## 5. Run it

```sh
naut run
```

```text
nautilus · sump — dashboard + tag API on http://localhost:8080 — Ctrl+C to stop
```

Open **http://localhost:8080** for the live dashboard, or leave it running
and use a second terminal for the rest of this step. Read the tags:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"LevelPct":0,"MinRun":30000,"PumpRun":false,"PumpStarts":0,"StartLevel":60,"StopLevel":20}
```

Over HTTP a `TIME` travels as milliseconds: `MinRun` is `30000`.

## 6. Drive it with forces

`LevelPct` is an input, so it belongs to the driver, and a write to it is
refused:

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"LevelPct","value":65}'
```

```text
tag LevelPct is a driver-owned input: its value is the field's, so a write is refused — force it (POST /api/forces) to hold a value during bring-up, or write the setpoint or command tag the logic reads instead
```

A **force** holds a tag at a value, against the driver, until you remove
it. Fill the sump:

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name":"LevelPct","value":65}'
```

```text
{"forces":[{"name":"LevelPct","value":65,"sinceMs":1793686399858}]}
```

The reply is the force table (`sinceMs` is when the force was set, so
yours will differ). The program sees the forced value from its next scan,
a tenth of a second later. Read the tags:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"LevelPct":65,"MinRun":30000,"PumpRun":true,"PumpStarts":1,"StartLevel":60,"StopLevel":20}
```

The pump started and the start was counted. Now drop the level below
`StopLevel` straight away and look at the timer, which is a program local:

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name":"LevelPct","value":15}'
curl -s localhost:8080/api/state | jq -c '.tags.PumpRun, .locals.minTimer'
```

```text
{"forces":[{"name":"LevelPct","value":15,"sinceMs":1793686403871}]}
true
{"ET":3900,"IN":true,"PT":30000,"Q":false}
```

Still running, with 3.9 s of the 30 s used (yours will differ: it is
however long you took between the two forces). Wait out the rest of
`MinRun` and read it again:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"LevelPct":15,"MinRun":30000,"PumpRun":false,"PumpStarts":1,"StartLevel":60,"StopLevel":20}
```

Setpoints, unlike inputs, take a plain write. Lower the start level, then
remove the force so the level goes back to what the driver says (the
loopback's `0`):

```sh
curl -s -X POST localhost:8080/api/tags -d '{"name":"StartLevel","value":50}'
curl -s -X DELETE localhost:8080/api/forces/LevelPct
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"forces":[]}
{"LevelPct":0,"MinRun":30000,"PumpRun":false,"PumpStarts":1,"StartLevel":50,"StopLevel":20}
```

A successful write answers `204 No Content`, so it prints nothing. Forces
are not retained: a restart comes up with none. Stop the controller with
Ctrl+C. The whole force API, the VS Code side of it and the lifetime rules
are in [Forcing](/guides/forcing/).

## 7. Build it

```sh
naut build
```

```text
built sump — a self-contained controller (run it like any binary; NAUTILUS_ADDR/NAUTILUS_TOKEN apply) — no git history embedded (no commits yet)
```

`./sump` is the whole controller: the program, the manifest and the
dashboard in one binary, with no `naut` and no toolchain needed where it
runs. Start it with `./sump` and it serves the same API on
`localhost:8080`; the tests are left out of it. "No git history embedded"
goes away once the project has a commit (`naut new` made it a git
repository); see [Program history](/guides/program-history/).

## Where next

- Put real I/O behind `LevelPct` and `PumpRun`: point `driver:` at the bus
  ([Modbus](/guides/modbus/), [EtherNet/IP](/guides/ethernet-ip/)). The
  program and the tests do not change.
- Add a high-level alarm on `LevelPct`: [Alarms](/guides/alarms/).
- Ship it: [Deployment](/guides/deployment/).
