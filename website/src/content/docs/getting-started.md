---
title: Getting started
description: Install the nautilus CLI, scaffold a project, and see a live scan loop with a dashboard in a few minutes.
---

:::tip[Brand new to this?]
Follow the [step-by-step tutorial](/tutorial/). It starts from an
empty laptop and goes as far as a running controller with an operator
screen. This page is the short reference version.
:::

## 1. Install the CLI

Download `naut`, put it on your `PATH`, and check it with `naut version`:
the commands for macOS, Linux and Windows, and the VS Code extension, are
in the lesson [Install the CLI and the editor](/learn/getting-started/install/).

The one binary is the whole toolchain: `naut new` (scaffold a
project), `run`, `test`, `check` (the CI gate: compiles every program and
cross-checks it against the manifest), `build`, `pull` (bring a controller's
running program back into the repo), `lsp` (the language server the VS Code
extension uses), and the `eip`, `modbus`, `sparkplug`, and `historian` tools.

## 2. Scaffold a project

```sh
naut new my-plant                      # the tour: 3 tasks, 3 IEC languages, simulated plant
naut new my-plant --template minimal   # one task, one program, one test file
naut new my-plant --template sdk       # Go project, for a custom field bus
naut new my-plant --template sdk-demo  # Go project with plant physics in Go
```

With a name and a `--template` it writes the project straight away, with
the defaults: the CI workflow, the VS Code setup and `git init`
(`--language fbd`, `ld` or `sfc` for the program, `--deploy` for the
container and Kubernetes files). Run it bare, or with only a name, for the
interactive form — it asks for the template, the program language, and the
features you want. `--no-input` never prompts.

A nautilus project is your logic and a manifest. Run, test, and ship it
with the CLI alone, no toolchain:

```sh
cd my-plant
naut run        # scan loop + dashboard + tag API on http://localhost:8080
naut test       # acceptance tests, in virtual time
naut check      # compile (the CI gate)
naut build      # emit ./my-plant — a self-contained controller binary
```

`nautilus.yaml` declares the tasks (one program file each, any language,
own scan rates), the tags by [role](/guides/tag-model/#which-role-fits-which-job),
the server, and the field driver.
`naut build` emits one deployable binary, with no Go toolchain anywhere.

`*_test.yaml` holds the acceptance tests, and they run against a **virtual
clock**, so a ten-second on-delay or a loop's settling time is asserted
exactly, deterministically, in milliseconds:

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

See [Testing](/reference/testing/) for the whole format.

Go is the **SDK**. Reach for `--template sdk` when you need a custom field
bus or richer simulation physics. That form is the same
runtime with the manifest written as code, and it's an ordinary Go
program:

```sh
cd my-plant
go mod tidy      # resolves github.com/joyautomation/nautilus from the proxy
go run .         # scan loop + tag API on http://localhost:8080
go test ./...    # Go tests that scan the program against an in-memory driver
```

The scaffolded `program_test.go` calls `rt.Scan()` one scan at a time
against the loopback `Memory` driver and asserts on the outputs. For timed
behaviour, a Go test can run on the same virtual clock as `naut test`
(`acceptance.NewClock` and `acceptance.NewScheduler`); the
[go-sdk example](https://github.com/joyautomation/nautilus/tree/main/examples/go-sdk)
does.

Open **http://localhost:8080** for the built-in live dashboard, or
`GET /api/state` for the raw tag snapshot. Setpoints are click-to-set right in
the tag table — click a value and type, or flip a BOOL with its toggle — so you
can drive the loop before there is an HMI. Inputs and outputs stay read-only in
the dashboard:
the driver rewrites an input before every scan and the logic rewrites an output
after, so an edit there would be discarded within a scan. `POST /api/tags` to
a `role: input` tag is refused for the same reason. To drive an input during
bring-up, [force it](/guides/forcing/): `POST /api/forces` with
`{"name": "LevelPct", "value": 65}` holds the value against the driver until you
remove the force. Every endpoint, with its status codes, is in the
[HTTP API](/guides/http-api/) reference.

## 3. Develop in VS Code

Install the **nautilus IEC 61131-3** extension for compile diagnostics as
you type, the graphical editors and live tag values: see
[the extension](/learn/getting-started/install/#the-vs-code-extension).

## 4. Make it yours

- Write control logic in `program.st`, or in `.ld`, `.fbd`, or `.sfc`; the
  graphical languages open in full diagram editors in VS Code.
- Assert on it in `*_test.yaml`, and keep asserting as you tune. The
  fixture comes from `nautilus.yaml`, so a retuned gain can't drift away
  from what the tests verify.
- Swap the simulated plant for real field I/O when you have hardware:
  delete the `sim` task and point `driver:` at the bus. The control logic
  doesn't change — it reads the same tags either way.
- Add an HMI with the SvelteKit component kit: faceplates, trends, and an
  SSE realtime client.
- Ship it as one binary. The scaffolded CI gates on `naut check`,
  `naut test`, and `naut build`; an SDK project's CI runs `go vet`,
  `naut check` (through `go run`), `go test`, and `go build` instead. Add
  `--deploy` to `naut new`
  for a Dockerfile, a redundant-pair Kubernetes manifest, and the workflow
  that ships a merged commit to the controller, or `--deploy-ssh` for a
  workflow that deploys each tag to a box over SSH with `naut deploy`.

To do all of that once, on a small program of your own, follow
[Your first program](/guides/first-program/): a sump pump with a
minimum-run timer and a start counter, from tags to tests to a running,
force-driven controller to a built binary, on one page.

## Next

- [Your first program](/guides/first-program/) — declare, write, test,
  run, force and build one small program, end to end.
- [Online edits](/guides/online-edits/) — change logic on a running
  controller from VS Code, and pull field edits back into git.
- [Testing](/reference/testing/) — virtual time, and the `*_test.yaml`
  format for asserting on timers and loop responses.
- [The tag model](/guides/tag-model/) — [which role fits which
  job](/guides/tag-model/#which-role-fits-which-job) (`input`, `output`,
  `setpoint` or `state`), how tags come to exist, and the one rule that
  bites.
- [Language reference](/reference/functions/) — evaluation semantics and
  every built-in operator, function, and function block.
