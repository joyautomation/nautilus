# Language conformance corpus

One directory per **feature** of the IEC 61131-3 implementation — a
standard function block, a function family, a ladder or SFC construct, a
documented deviation. Each directory is a tiny manifest project that
carries **the same logic in every language that can express it**, plus one
acceptance suite that asserts the behaviour in virtual time. The
acceptance harness (`acceptance/`, the engine behind `naut test`) runs it;
`go test ./lang/conformance/` walks every directory, so CI gets the whole
corpus for free.

```sh
go test ./lang/conformance/                             # the whole corpus
go test ./lang/conformance/ -run 'TestConformance/fb-ton' -v   # one feature
go run ./cmd/naut test lang/conformance/fb-ton          # the same suite, as a user would
go run ./cmd/naut check lang/conformance/fb-ton         # offline validation of the project
go run ./lang/conformance -matrix                       # the feature × language table
```

## Why one project per feature, in every language

`docs/functions.md` claims the behaviours; each directory here is a
runnable example of one claim. Ladder and FBD lower to ST through the
transpilers, so a divergence between the LD→FBD→ST path and native ST is
exactly the class of bug those paths can hide — and the only way to see it
is to run the same logic through each and assert them **together**. One
YAML step asserts `st_Q`, `ld_Q` and `fbd_Q` at once; a row of three
results that disagree is the finding.

## Layout of a feature

```
lang/conformance/<feature>/
  nautilus.yaml           the manifest: one task per language, the shared tags
  <feature>.st            Structured Text program    (task "main")
  <feature>.ld            ladder program             (task "ld")
  <feature>.fbd           FBD program                (task "fbd")
  <feature>.sfc           SFC program                (task "sfc") — only where SFC can express it
  <feature>_test.yaml     the acceptance suite
```

Names are not negotiable: the walker and the matrix look for
`<feature>.<lang>` (`st`, `ld`, `fbd`, `sfc`) and `*_test.yaml` **by name**,
where `<feature>` is the directory's base name. A program file under any
other name is not a language column.

Rules every feature follows (`fb-ton/` is the worked example — copy it):

1. **One task per language, same scan.** The first task in `tasks:` is
   always the runtime's `main` task (do not give it a `name:` — `naut
   check` warns). Put the ST program first; name the others `ld`, `fbd`,
   `sfc`. Every task gets the same `scan:` so one `advance:` means the same
   number of scans in each language. `1ms` is the right scan for timer
   blocks (it is the timer's own resolution); use what the feature needs.
2. **Tag-prefix rule.** Shared *inputs* are plain tags with no prefix
   (`IN`, `PT`, `CU`, …). Every *output* a program writes is prefixed by
   its language — `st_Q`, `ld_Q`, `fbd_Q`, `sfc_Q` — and each program
   writes **only its own prefix**. Then one `expect:` lists all three (or
   four) side by side.
3. **Same logic, every language.** The programs must do the same thing in
   the most natural form of each language: in ST a call and two
   assignments, in LD a rung with the block in it (`IN t:TON(PT := PT,
   ET => ld_ET) ( ld_Q )`), in FBD the block and two output wires. Do
   not add logic in one language that the others lack. If a language
   cannot express the feature at all (SFC and a bare counter, say), leave
   that file out — a blank cell in the matrix is honest; a padded-out
   program is not.
4. **Distinct POU names.** Each program needs its own `PROGRAM` name
   (`TonSt`, `TonLd`, `TonFbd`); the runtime routes by POU name, so two
   tasks with the same one is a load error.
5. **Every language file is a task.** The walker fails a directory where
   `<feature>.ld` exists but no task runs it — a ✓ in the matrix must mean
   "asserted", never "present".
6. **The suite asserts behaviour, not direction.** Encode the edges from
   the standard's timing diagrams (the plan's edge list in
   `randd/handoffs/TEST-PLAN-HANDOFF.md` §1.2): the scan before, the scan
   of, the scan after; first scan with the input already TRUE; reset
   dominance; what holds after the output rose. Each `- name:` says which
   edge it pins.

## Writing the suite

`docs/testing.md` is the YAML reference; `examples/lift-station/*_test.yaml`
is the house style. What matters here:

- **Every step asserts every language.** `expect: { st_Q: true, ld_Q:
  true, fbd_Q: true }` — never one prefix alone, or the other paths are
  untested on that edge.
- **TIME is milliseconds.** A `TIME` tag is given and compared as a plain
  integer in ms: `init: 5000` seeds `PT` to `T#5s`, `expect: { st_ET:
  4999 }` reads ET. (Programs still declare it `PT : TIME` in
  `VAR_EXTERNAL`; the manifest seeds against that declaration.)
- **The first scan is at t = 1 × scan, not t = 0.** The scheduler fires a
  task one interval in. So "the scan IN is first seen TRUE" is `given:
  { IN: true }` then `scans: 1`, and `advance: PT-1ms` / `advance: 1ms`
  from there land on PT−1 and PT exactly. `fb-ton_test.yaml` shows the
  shape.
- **`scans:` counts the main task** (the ST program); the other tasks, on
  the same scan rate, scan at the same instants, main first then
  declaration order.
- **Tag roles.** Inputs and outputs here are `role: state` with an
  `init:` (a `state`/`setpoint` tag must be seeded), written straight to
  the tag store by `given:`. Use `role: input` only when the feature is
  about the driver image.
- **A scan fault fails the test** without being asked for — that is the
  assertion for `st-mux-fault`-style features. Read the failure text to
  see how the harness reports it before writing the suite.

## Pinning a deviation

Where the implementation deliberately departs from the standard (the
documented ones live in `lang/ir/builtins_std.go` and `docs/functions.md`:
every INT width is int64 and nothing wraps, ROL/ROR rotate over 64 bits,
string positions clamp), the corpus **pins the deviation as a test** — a
feature whose suite asserts the actual, documented behaviour, with a
comment at the top of the `*_test.yaml` saying which decision it pins and
where it is written down. The point is that the deviation is a decision,
not an accident: changing it later fails a named test, and the person
changing it edits the pin on purpose. A pin is not an endorsement; the
comment should say what the standard (and the vendors) do instead.

## Adding a feature, step by step

1. `cp -r lang/conformance/fb-ton lang/conformance/<feature>` and rename
   the four `fb-ton.*` files to `<feature>.*` (`git mv` is fine).
2. In `nautilus.yaml`: set `name:`, point each task's `program:` at the
   renamed files, delete the task (and file) for any language that cannot
   express the feature, and declare the shared inputs plus one output set
   per language, prefixed.
3. Rewrite each program to the feature, keeping the three in lockstep
   (rule 3) and the POU names distinct (rule 4).
4. Write `<feature>_test.yaml` from the edge list; every step asserts
   every prefix.
5. `go run ./cmd/naut check lang/conformance/<feature>` until clean, then
   `go test ./lang/conformance/ -run 'TestConformance/<feature>' -v`.
6. `go run ./lang/conformance -matrix` — the new row appears with its
   test count. Paste the table into the PR description.

## The matrix

`go run ./lang/conformance -matrix` prints a markdown table, feature ×
language, ✓ where `<feature>.<lang>` exists and the number of tests in the
directory's `*_test.yaml` files. It is generated from the directory
listing — nothing to keep in sync — and `TestMatrix` in
`conformance_test.go` checks every feature has at least one language file
and one test. Run it from the repo root (it finds `lang/conformance/`
under the cwd) or pass `-dir`.

Current table:

<!-- go run ./lang/conformance -matrix -->
| Feature | ST | LD | FBD | SFC | Tests |
|---|:-:|:-:|:-:|:-:|--:|
| `fb-ton` | ✓ | ✓ | ✓ |   | 6 |
| `sfc-alt-priority` |   |   |   | ✓ | 7 |
| `sfc-final-scan` |   |   |   | ✓ | 4 |
| `sfc-jump-back` |   |   |   | ✓ | 4 |
| `sfc-qualifiers` |   |   |   | ✓ | 7 |
| `sfc-sim-div-conv` |   |   |   | ✓ | 6 |
| `sfc-step-t` |   |   |   | ✓ | 5 |
