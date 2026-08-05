---
title: Getting started
description: Install the nautilus CLI, scaffold a project, and see a live scan loop with a dashboard in a few minutes.
---

**Prerequisites:** Go 1.24+ with `$(go env GOPATH)/bin` on your `PATH`, and
VS Code for the editor experience.

## 1. Install the CLI

```sh
go install github.com/joyautomation/nautilus/cmd/nautilus@latest
```

This gives you `nautilus new` (scaffold a project), `nautilus check`
(headless Structured Text compile for CI), and `nautilus lsp` (the language
server the VS Code extension uses).

## 2. Scaffold a project

```sh
nautilus new my-plant                      # the tour: 3 tasks, 3 IEC languages, simulated plant
nautilus new my-plant --template minimal   # one task, one program, one test
nautilus new my-plant --template sdk       # Go project, for a custom field bus
nautilus new my-plant --template sdk-demo  # Go project with plant physics in Go
```

Run it bare for the interactive form — it asks for the template, the
program language, and the features you want.

A nautilus project is your logic and a manifest. Run, test, and ship it
with the CLI alone — no toolchain:

```sh
cd my-plant
nautilus run        # scan loop + dashboard + tag API on http://localhost:8080
nautilus test       # acceptance tests, in virtual time
nautilus check      # compile (the CI gate)
nautilus build      # emit ./my-plant — a self-contained controller binary
```

`nautilus.yaml` declares the tasks (one program file each, any language,
own scan rates), the tags by role, the server, and the field driver.
`nautilus build` emits one deployable binary — no Go toolchain anywhere.

`*_test.yaml` holds the acceptance tests, and they run against a **virtual
clock** — so a ten-second on-delay or a loop's settling time is asserted
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

Go is the **SDK**, not the base — reach for `--template sdk` when you need
a custom field bus or richer simulation physics. That form is the same
runtime with the manifest written as code, and it's an ordinary Go
program:

```sh
cd my-plant
go mod tidy      # resolves github.com/joyautomation/nautilus from the proxy
go run .         # scan loop + tag API on http://localhost:8080
go test ./...    # the program's acceptance tests, on the same virtual clock
```

Open **http://localhost:8080** for the built-in live dashboard, or
`GET /api/state` for the raw tag snapshot. Setpoints are click-to-set right in
the tag table — click a value and type, or flip a BOOL with its toggle — so you
can drive the loop before there is an HMI. Inputs and outputs stay read-only:
the driver rewrites an input before every scan and the logic rewrites an output
after, so an edit there would be discarded within a scan.

## 3. Develop in VS Code

Install **nautilus IEC 61131-3** from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=joyauto.vscode-iec)
or [Open VSX](https://open-vsx.org/extension/joyauto/vscode-iec) —
currently on the **pre-release** channel, so use *Install Pre-Release
Version*. With your project open and the controller running you get compile
diagnostics as you type, go-to-definition, hover, completion, and live tag
values next to identifiers in your program.

## 4. Make it yours

- Write control logic in `program.st` — or `.ld` / `.fbd`; the graphical
  languages open in full diagram editors in VS Code.
- Assert on it in `*_test.yaml`, and keep asserting as you tune. The
  fixture comes from `nautilus.yaml`, so a retuned gain can't drift away
  from what the tests verify.
- Swap the simulated plant for real field I/O when you have hardware:
  delete the `sim` task and point `driver:` at the bus. The control logic
  doesn't change — it reads the same tags either way.
- Add an HMI with the SvelteKit component kit: faceplates, trends, and an
  SSE realtime client.
- Ship it as one binary. The scaffolded CI gates on `nautilus check`,
  `nautilus test`, and `nautilus build`.

## Next

- [Testing](/reference/testing/) — virtual time, and the `*_test.yaml`
  format for asserting on timers and loop responses.
- [The tag model](/guides/tag-model/) — how tags come to exist, which role
  fits which job, and the one rule that bites.
- [Language reference](/reference/functions/) — evaluation semantics and
  every built-in operator, function, and function block.
