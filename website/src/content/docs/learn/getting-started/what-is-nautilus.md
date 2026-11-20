---
title: What nautilus is
description: "What you can build with nautilus, how a project is laid out, and which lessons to take first whether you come from Logix, SCADA or software."
sidebar:
  order: 1
---

nautilus is a toolchain for industrial control. You write the control logic
as text files in a project folder, you test it, you run it on your laptop,
and you ship it as one program to a controller. That is the whole loop, and
the lessons in this course walk it from the start.

## What a project is

A project is a folder:

```text
motor/
  nautilus.yaml      the controller: its tasks, its tags, its field driver
  program.st         the logic, in one of four IEC 61131-3 languages
  motor_test.yaml    acceptance tests that assert on the logic
  motor.mimic.json   an operator screen (optional)
```

Everything is a plain text file, so it works with git: you can diff a
change, review it, and see who changed what. There is no project database
and no vendor IDE to open it in. The **VS Code extension** adds diagrams,
live values and diagnostics, but the files are the product.

## What the `naut` command does

One program does the whole job:

| Command | What it does |
| --- | --- |
| `naut new` | make a project |
| `naut check` | compile every program and cross-check it against the project |
| `naut test` | run the acceptance tests on a virtual clock |
| `naut run` | run the controller on your computer, with a dashboard in the browser |
| `naut build` | produce one self-contained controller program |
| `naut deploy` | install and update that program on a Linux box over SSH |

## Words you will meet

- **Controller**: the program that runs your logic in a loop.
- **Scan**: one pass of that loop, over and over, typically every 10 to
  100 ms. The logic does not wait for events; it re-reads the world every
  scan.
- **Tag**: a named value, such as `MotorRun`. Logic, screens, tests and
  other systems all meet at the tags.
- **Task**: a program together with how often it scans.

These come back in every lesson; the first lessons use each one.

## Where do you start?

You can take the lessons in order. If you are coming from somewhere else,
these pages say what carries over:

- **From Studio 5000 (Logix):**
  [Coming from Studio 5000](/coming-from/studio-5000/) maps every
  instruction you know to its nautilus form.
- **From Siemens or CODESYS:** [TIA Portal](/coming-from/tia-portal/) and
  [CODESYS](/coming-from/codesys/).
- **From SCADA (Ignition, Kepware, Wonderware):** [Coming from
  SCADA](/coming-from/scada/) maps tag providers, UDTs, alarm pipelines,
  views and OPC UA connections to their nautilus form.
- **From software development:** every value is also available over an
  [HTTP API](/guides/http-api/), the project builds in CI like any other,
  and [an AI agent](/guides/ai-assistants/) can work on it from the files.

## Next

[Install the CLI and the editor](/learn/getting-started/install/).
