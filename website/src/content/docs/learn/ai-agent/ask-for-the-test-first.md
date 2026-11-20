---
title: Ask for the test first, then review what it chose
description: "Hand an agent a task by writing the test that defines it, check its work with naut check and naut test, and find the choices the tests did not settle."
sidebar:
  order: 2
---

An agent will write control logic that compiles. Whether it is the logic you
meant is a different question, and a good way to answer it is to write down
what you meant first, as a test, before the agent writes anything. About
fifteen minutes. You need a project with `AGENTS.md` (the [last
lesson](/learn/ai-agent/what-your-project-tells-an-agent/)) and any coding
agent.

## Write the test, and watch it fail

The job: a tank temperature alarm that waits ten seconds above a threshold. In
a new project (`naut new alarm --template minimal`), declare the three tags in
`nautilus.yaml` in place of the sample ones:

```yaml
  - { name: TempC,       role: input,    type: REAL, init: 20.0, unit: "°C", desc: Tank temperature }
  - { name: HighTempSP,  role: setpoint, type: REAL, init: 90.0, unit: "°C", desc: High temperature alarm threshold }
  - { name: HighTempAlm, role: output,   type: BOOL, init: false, desc: High temperature alarm }
```

Replace `program.st` with an empty program, which does nothing yet:

```st
PROGRAM Alarm
END_PROGRAM
```

and replace the scaffold's test file with `alarm_test.yaml` (delete the old
one): the behaviour, in virtual time:

```yaml
tests:
  - name: the alarm waits its full 10 s above the threshold
    steps:
      - given: { TempC: 95.0 }
        advance: 9.5s
        expect: { HighTempAlm: false }
      - advance: 1s
        expect: { HighTempAlm: true }

  - name: a brief spike does not alarm
    steps:
      - given: { TempC: 95.0 }
        advance: 5s
      - given: { TempC: 20.0 }
        advance: 10s
        expect: { HighTempAlm: false }

  - name: the alarm clears when the temperature falls back
    steps:
      - given: { TempC: 95.0 }
        advance: 11s
        expect: { HighTempAlm: true }
      - given: { TempC: 20.0 }
        advance: 1s
        expect: { HighTempAlm: false }
```

```sh
naut test
```

```text
3 tests, 1 passed, 2 failed
```

(Above that line, each failing test is drawn with its tag's history over the
virtual time.) One passes by accident: with no logic the alarm is false, which
is all the "brief spike" test asks. `naut check` also warns about the two tags
no program uses:

```text
nautilus.yaml:31:13: warning: nautilus.yaml declares setpoint "HighTempSP", which no program binds — dead, or a stale generated entry
nautilus.yaml:32:13: warning: nautilus.yaml declares output "HighTempAlm", which no program binds — dead, or a stale generated entry
```

`naut new` made a git repository but has not committed anything yet. Commit the
project with the test in it: that is the task, and the point `git diff` will
compare the agent's work with.

```sh
git add -A
git commit -m "alarm: the test"
```

(git asks once for your name and email if you have never set them.)

## Hand it over

Tell the agent what you want and where its limits are. Naming what it must not
change keeps its work inside the program:

> A tank temperature alarm. `alarm_test.yaml` states what I want, in virtual
> time, and it fails right now. Write the logic in `program.st` so that
> `naut test` passes. The tags are in nautilus.yaml: don't change it or the
> test file.

The agent reads `AGENTS.md` itself (Claude Code through `CLAUDE.md`, Codex
directly), so you do not paste the rules into the request.

## What it wrote

Here is what a small model (Claude Haiku) wrote. `git diff --stat` shows
only `program.st` changed, 13 lines:

```st
PROGRAM Alarm
(* High tank temperature alarm.
   The alarm sets only after TempC has stayed above HighTempSP for a full
   10 s. A timer restarts whenever the temperature is at or below the
   threshold, so a brief spike never reaches 10 s. The alarm clears in the
   same scan the temperature falls back to the threshold or below. *)
VAR
  HighTempTmr : TON;
END_VAR

(* Debounce: the timer runs only while the temperature is above the
   setpoint. Its output is the alarm. *)
HighTempTmr(IN := TempC > HighTempSP, PT := T#10S);
HighTempAlm := HighTempTmr.Q;
END_PROGRAM
```

```text
naut check: 1 file(s), 0 with errors
alarm_test.yaml
  ok    the alarm waits its full 10 s above the threshold
  ok    a brief spike does not alarm
  ok    the alarm clears when the temperature falls back

3 tests, 3 passed, 0 failed
```

`TON` is the on-delay timer from [the timer
lesson](/learn/interactive/b3-horn-delay/): `Q` turns true after `IN` has been
true for `PT`. It followed the file: it read the threshold from the setpoint tag instead of
writing `90.0`, declared no tag again, wrote the output in one place, commented
the logic, and used a standard `TON`.

## Review what it chose

The tests pass. That means the agent did everything the tests ask, and nothing
more: it does not mean it did what you meant. Read the program for the choices
the tests did not make. Here there is one: **what happens at exactly 90 °C.**
The agent chose `>`, so at the threshold there is no alarm. Your three tests
pass with `>` and with `>=` alike.

Decide what you want, and write it as a test:

```yaml
  - name: at exactly the threshold there is no alarm
    steps:
      - given: { TempC: 90.0 }
        advance: 20s
        expect: { HighTempAlm: false }
```

With the agent's program that passes. Change the `>` in `TempC > HighTempSP` to `>=` and
the same file fails (`FAIL  at exactly the threshold there is no alarm`). The test
now says what you decided, whoever, or whatever, edits the logic next.

## A checklist for reviewing an agent's work

- `git diff`: did it change only what you asked? A request that said "don't
  touch the manifest" is only worth something if you look.
- `naut check` with no new warnings. It compiles and checks structure; it does
  not run the logic.
- `naut test` passes, and the tests are the ones you wrote, not tests the agent
  rewrote to pass.
- The choices the tests leave open (boundaries, what happens on power-up, what
  an alarm does when its input goes bad): find them, decide, add a test.
- For ladder, FBD and SFC, open the result as a diagram (**Open With** in VS
  Code): a rung that is hard to follow there is the thing to send back.

## Next

The [AI assistants guide](/guides/ai-assistants/) explains why a diagram can
compile and still look wrong, and what the rules for ladder, FBD and SFC are.
