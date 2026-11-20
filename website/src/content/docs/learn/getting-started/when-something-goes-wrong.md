---
title: When something goes wrong
description: "How to read the messages naut check, naut test and naut run give you, the common first-hour mistakes, and what to try next."
sidebar:
  order: 5
---

Most first-hour problems are one of four kinds. Each tool says where.

## The program does not compile: `naut check`

`naut check` prints one line per problem, with the file, line and column:

```text
program.st:12:5: undeclared identifier "LevelPc"
```

Read it from the left: which file, where in it, what is wrong. The usual
causes are a misspelt tag or variable name, a type mismatch (`cannot assign
REAL to BOOL`), or a missing `;`. Fix the first one and run `naut check`
again; later errors are often caused by the first.

`naut check` is also what your continuous integration runs, so a project
that passes it locally passes there.

## A test names something that does not exist: `naut test`

```text
sump_test.yaml:5 — given: no tag "Sensor" in this project
```

A test can only set and check tags the project declares. The name in the
test must match a `name:` in `nautilus.yaml` exactly. When a test fails an
expectation, it shows the line of the expectation, the virtual time, and
the value the tag really had, with a short history of it, so you can see
what the logic did.

## The controller does not behave: `naut run`

- **Port in use.** `tag api: localhost:8080 is in use (another naut run?),
  so this one is on localhost:8081`. Another controller is already on that
  port. `naut run` moved to the next free one and printed it; use that
  address, or stop the other controller.
- **A value you wrote did not stick.** Writing an `input` is refused (see
  [the last lesson](/learn/getting-started/dashboard-and-tags/)); write the
  setpoint the logic reads, or force the input.
- **`not rendered: no Chrome, Chromium or Edge`.** `naut check` and
  `naut hmi build` draw a screen in a browser to look for layout problems.
  With none on the machine they say so and carry on: the screen is still
  checked against your tags. Install one of those browsers, or ignore it.
- **A tag reads but the screen shows nothing.** The name in the screen or
  the logic is misspelt: `naut check` reports a name a screen binds that
  the project does not declare.

## The install does not work

"command not found", a macOS security prompt, a Windows SmartScreen
warning, and "address already in use" are covered, step by step, in the
[tutorial's troubleshooting section](/tutorial/#when-something-goes-wrong).

## Still stuck

Ask in the project's [issue
tracker](https://github.com/joyautomation/nautilus/issues) with the
command you ran and everything it printed.

## Next

[Your first program](/guides/first-program/).
