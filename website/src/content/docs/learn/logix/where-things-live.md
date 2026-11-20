---
title: Where things live
description: "The Studio 5000 controller organizer, laid out as the files of a nautilus project: controller, tasks, programs, tags, data types, Add-On Instructions and I/O."
sidebar:
  order: 2
---

In Studio 5000 everything is in one tree, the Controller Organizer. In
nautilus the same things are files in a folder. This lesson walks a project
you imported in [the last
lesson](/learn/logix/read-a-logix-project/), file by file, against the tree
you know. About ten minutes.

## The map

| In Studio 5000 | In the project |
| --- | --- |
| Controller (name, processor, revision) | `nautilus.yaml`: `name:`, and `target: logix:` with the controller, processor and revision |
| Tasks (continuous, periodic) | `tasks:` in `nautilus.yaml`, one program file per task, each with its `scan:` period |
| Programs and routines | one **program** file per routine, `MainProgram.ld`: ladder written as text |
| Controller tags and program tags | `tags/logix.yaml`, one line per tag with its description, and, in each program, a `VAR_EXTERNAL` list of the tags it uses |
| A timer or counter tag | a local of the program: `AcceptTmr : TON;` under `VAR` |
| User-defined data types | `lib/logix_types.st`: a `TYPE` with a `STRUCT` |
| Add-On Instructions | a `FUNCTION_BLOCK` in `lib/*.ld`, instantiated by name |
| I/O configuration (the modules) | the driver: `hardware.L5X` for what the import keeps, or a field driver in `nautilus.yaml` |

## Read the files

`nautilus.yaml` is the controller. Its header says what produced it, and
`target: logix:` keeps what is needed to write the project back:

```yaml
name: linecontroller
tasks:
  - program: MainProgram.ld
    scan: 10ms # continuous task MainTask in the export
tag-files: [tags/logix.yaml]
target:
  logix:
    controller: LineController
    processor: 1769-L33ER
    revision: "36.11"
```

`tags/logix.yaml` holds the tags, with their `role:`: an `output` for a tag
the imported logic writes, an `input` for the rest, and the tag's own
description from the export (which a live browse of a controller cannot
recover):

```yaml
- { name: Line_Accept, role: output, init: false, desc: "Line has accepted the skid's transfer request" }
```

`lib/logix_types.st` is the data types: a `UDT` is a `TYPE`, a `STRUCT`:

```st
TYPE
  LineStatus : STRUCT (* Mode/alarm summary the skid polls alongside the handshake tags *)
    Mode : DINT;
    Alarm : BOOL;
  END_STRUCT;
END_TYPE
```

`MainProgram.ld` starts with the tags it uses, so a program is honest about
what it touches:

```st
VAR_EXTERNAL
    Line_Accept    : BOOL;
    Line_Request   : BOOL;
    Line_Status    : LineStatus;
END_VAR
VAR
    AcceptTmr : TON;
END_VAR
```

A program's own variables, like `AcceptTmr`, are its private state; the
tags are the shared values every program, screen and test meets at.

## The files you did not write

A project made by `naut new` or an import also has:

- `AGENTS.md` and `CLAUDE.md`: guidance for AI coding assistants working on
  the project (see [AI assistants](/guides/ai-assistants/)).
- `.github/`: a continuous-integration workflow that runs `naut check`,
  `naut test` and `naut build` on every change.
- `.vscode/`: editor setup, so VS Code offers the extension.
- `README.md` and `.gitignore`.

None of them is the program; you can read them once and forget them.

## Next

Write a test for the imported logic with [the test lesson](/learn/programming/test-it-in-virtual-time/),
or learn the [seal-in](/learn/interactive/b2-seal-in/) the way a Logix
programmer writes it. The [Studio 5000
page](/coming-from/studio-5000/) maps the instructions, and [Allen-Bradley
Logix](/guides/logix/) covers writing back to a controller.
