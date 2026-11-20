---
title: Test it, in virtual time
description: "Write acceptance tests for a program, run them on a virtual clock so a 5 second delay takes no real time, and read a failure."
sidebar:
  order: 1
---

Control logic that nobody tests is logic that works until the day it
matters. In this lesson you write tests for the horn-and-motor program from
[the timer lesson](/learn/interactive/b3-horn-delay/) and watch them fail
usefully. About fifteen minutes. A test file is plain text beside the
program, it runs with one command, and it runs on a **virtual clock**: a
five-second delay takes a fraction of a millisecond of real time, and the
result is exact every time.

## A test file

A project's tests live in files named `*_test.yaml`. This is
`horn_test.yaml`, beside the program and `nautilus.yaml`:

```yaml
tests:
  - name: the horn sounds as soon as Start is pressed
    steps:
      - given: { StartPB: true }
        advance: 0.3s
        expect: { Horn: true, MotorRun: false }

  - name: the motor starts 5 seconds after Start
    steps:
      - given: { StartPB: true }
        advance: 0.2s
      - given: { StartPB: false }
        advance: 4.3s
        expect: { MotorRun: false }
      - advance: 1s
        expect: { MotorRun: true, Horn: false }

  - name: Stop during the warning cancels the start
    steps:
      - given: { StartPB: true }
        advance: 0.2s
      - given: { StartPB: false }
        advance: 2s
      - given: { StopPB: true }
        advance: 0.3s
      - given: { StopPB: false }
        advance: 8s
        expect: { MotorRun: false, Horn: false }
```

Read it from the top:

- A test has a `name` and a list of `steps`. Name it for the behaviour, not
  the mechanism: the name is what you read when it fails.
- `given` sets tags before the step runs: here, pressing and releasing the
  buttons. The fixture is `nautilus.yaml`, so every tag starts at its
  `init:` value and the scan rate is the project's. Nothing is restated.
- `advance: 4.3s` runs 4.3 seconds of **virtual** time: the clock the timer
  counts on moves exactly that far, with no waiting.
- `expect` is checked at the end of the step. The second test says: 4.5
  seconds after Start the motor is still off, and one second later it is on
  and the horn has stopped.

## Run them

```sh
naut test -v
```

```text
horn_test.yaml
  ok    the horn sounds as soon as Start is pressed    3 scans, 0.3s
  ok    the motor starts 5 seconds after Start         55 scans, 5.5s
  ok    Stop during the warning cancels the start      105 scans, 10.5s

3 tests, 3 passed, 0 failed
```

`-v` prints the virtual time each test covered: 10.5 seconds of process
time for the third test, which ran in a fraction of that. A test can wait an
hour of virtual time for a long timer and finish in a fraction of a second.

## Break it on purpose

A test is worth what it catches. In `program.ld`, change `T#5S` to `T#2S`, so
the motor starts too soon, and run the tests again:

```text
horn_test.yaml
  ok    the horn sounds as soon as Start is pressed
  FAIL  the motor starts 5 seconds after Start
        horn_test.yaml:14 — step 2 (line 12), t=4.5s
          MotorRun = true, want false   (expectation failed)

          MotorRun  — Motor contactor
                  0.2s       0.8s       1.4s         2s       2.6s       3.2s       3.8s       4.5s
                 false      false      false      false       true       true       true       true

  ok    Stop during the warning cancels the start

3 tests, 2 passed, 1 failed
```

The failure names the test, the line of the expectation, the virtual time,
and the value the tag really had, then draws the tag's history so you can see
what the logic did: `MotorRun` turned on at about 2 s. Put `T#5S` back and
the suite passes. `naut test` exits non-zero on a failure, which is what a
continuous-integration job gates on.

## What to test

- **The behaviour the operator sees**, in terms of tags: "after Start, 5
  seconds later the motor runs".
- **The edges of it**: just before the delay and just after (the second test
  checks 4.5 s and 5.5 s), and the interruption (Stop in the middle).
- **A mistake you have made**, once you find it: a test is how a bug stays
  fixed.

[Testing](/reference/testing/) lists every step key (`scans`, `until` with
`hold`, `force`, alarms and more) and every kind of `expect`.

## Next

[Watch and force tags while the program runs](/learn/getting-started/dashboard-and-tags/#drive-an-input-with-a-force):
tests drive a program in virtual time; forcing drives it live.
