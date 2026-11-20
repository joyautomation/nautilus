---
title: Your first controller, running
description: "Make a project with naut new, run the controller, watch the tags in the dashboard, change a setpoint, and run the project's tests."
sidebar:
  order: 3
---

In this lesson you make a project, start its controller on your computer
and watch it run. It takes about ten minutes. You need `naut` installed
([the last lesson](/learn/getting-started/install/)).

## Make a project

```sh
naut new hello --template minimal
cd hello
```

`naut new` writes a folder with a handful of files. Three matter:

- `nautilus.yaml`: the project. It lists one **task** (`program.st`, scanning
  every 100 ms) and three **tags**: `Sensor` (an input), `Setpoint` (a
  value an operator can change) and `Actuator` (an output).
- `program.st`: the logic, in Structured Text. It sets
  `Actuator := LIMIT(0.0, Setpoint - Sensor, 100.0)`: the actuator follows
  how far the sensor is below the setpoint, clamped to 0 to 100.
- `hello_test.yaml`: tests for that logic.

Check that it compiles:

```sh
naut check
```

You should see `naut check: 1 file(s), 0 with errors`.

## Run it

```sh
naut run
```

You should see a line that ends `tag API on http://localhost:8080` and
`Ctrl+C to stop`. The controller is now scanning every 100 ms. Open
<http://localhost:8080> in a browser: the **dashboard** lists every tag with
its live value, and the scan timing. `Setpoint` is 50, `Sensor` is 0, and
`Actuator` is 50, as the logic says: 50 − 0.

If port 8080 is already in use, `naut run` says so and moves to the next
free port; use the address it prints.

## Change something while it runs

In the dashboard, click the value of `Setpoint` and type `70`. Within one
scan `Actuator` becomes `70`. You changed a value the logic reads, and the
logic answered.

You cannot do the same to `Sensor`: it is an `input`, which belongs to the
field driver, so the dashboard shows it read-only. The next lesson shows how
to drive an input while you are testing.

Stop the controller with `Ctrl+C`.

## Run the tests

```sh
naut test
```

```text
hello_test.yaml
  ok    actuator tracks the error and clamps
  ok    actuator stays inside its clamp

2 tests, 2 passed, 0 failed
```

The tests ran in virtual time, without waiting for real time to pass. A
later lesson shows how to write them.

## Next

[Read the dashboard and the tags](/learn/getting-started/dashboard-and-tags/).
