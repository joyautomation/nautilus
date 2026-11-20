---
title: What your project tells an agent
description: "The AGENTS.md that naut new writes, what is in it, how to add or refresh it, and where to put your own rules."
sidebar:
  order: 1
---

A nautilus project is text, so a coding agent (Claude Code, Codex and others)
can read it and write it. What an agent cannot know is how *this* toolchain
wants it done: which commands check its work, how tags are meant to be
declared, what a good ladder rung looks like. The project tells it, in a file.
About ten minutes.

## The file `naut new` writes

```sh
naut new sorter --template minimal
cd sorter
ls
```

```text
AGENTS.md  CLAUDE.md  nautilus.yaml  program.st  README.md  sorter_test.yaml
```

(and hidden `.github/`, `.vscode/` and `.gitignore`). `AGENTS.md` is the
guidance, in a form most agents read on their own. `CLAUDE.md` is four lines
that import it, because Claude Code looks for that name:

```text
This project's guidance for AI assistants lives in AGENTS.md, shared by
every tool that reads it. Read it before changing any program:

@AGENTS.md
```

## What is in it

For the minimal Structured Text project it is about 180 lines. Its headings:

```text
Check every change
Project layout
Tags
Pick the language for new logic
Structured Text (.st)
Ladder (.ld) (not used in this project yet)
Function Block Diagram (.fbd) (not used in this project yet)
Sequential Function Chart (.sfc) (not used in this project yet)
Operator screens (not used in this project yet)
Tests (*_test.yaml)
Before you finish
```

The file only carries the full rules for what the project uses. This one has
Structured Text with a worked example; the ladder, FBD, SFC and screens
sections are short stubs that say they are not used yet. Add a `.ld` file
and run `naut agents`, and the ladder rules arrive.

The most useful part is the first section. It tells the agent to run
`naut check` after every edit and to treat a warning as a bug, and to run
`naut test` when behaviour changes. `naut check` does not run the logic, only
compiles it and checks structure, so the file also says when a test is
needed. And at the end, "Before you finish": no errors, no new warnings, a
test for every timer and permissive, and a comment on every rung.

## Add it to an older project

A project made before the file existed, or imported from elsewhere, has none. (To
see it, delete `AGENTS.md` and `CLAUDE.md` from a scratch project.)

```sh
naut agents -check
```

```text
AGENTS.md: missing
CLAUDE.md: missing
```

(and exit code 1; with both present it says `current` and exits 0). Write it:

```sh
naut agents
```

```text
AGENTS.md: wrote
CLAUDE.md: wrote
```

`naut agents` writes `AGENTS.md` and a `CLAUDE.md` that imports it. If an
`AGENTS.md` that `naut` did not write is already there, it refuses (exit code 1)
and says why: `naut agents -append` adds the guidance below your text, and
`-force` replaces the file.

## Your own rules go outside the markers

The nautilus text sits between two comment markers, the first line and the last
line of the file:

```markdown
<!-- nautilus:begin (naut …): `naut agents` keeps the text between these markers current. Write this project's own notes above or below them. -->
…
<!-- nautilus:end -->
```

When a newer `naut` has newer guidance, `naut check` warns that the file is
older than your `naut`, and `naut agents` replaces what is between the markers.
Anything above or below them is yours and is kept: tag naming conventions,
which program owns which equipment, what a rung comment must say.

## Next

[Ask for the test first](/learn/ai-agent/ask-for-the-test-first/): how to hand
a task to an agent so you can tell whether it did it. The [AI assistants
guide](/guides/ai-assistants/) covers the diagram rules in full.
