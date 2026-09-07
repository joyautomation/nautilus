# Pump station — generated tags, UDTs, and five generators

A six-pump station built the way `docs/design/tags.md` §2.2 describes: a UDT
declared once in ST, a **config table plus an expander** that writes
`tags/pumps.yaml`, and one line in the manifest to compose it.

```
motor.st            the Motor UDT — a root .st with no PROGRAM is a library,
                    so `type: Motor` in the manifest resolves against it
nautilus.yaml       tag-files: [tags/pumps.yaml] + 2 hand-authored tags
station.st          per-pump run hours, station rollups
sim.st              stands in for the field bus
station_test.yaml   acceptance tests addressing UDT fields (P101.Running)

tools/tags.go       ← the generator. 9 table rows.
tags/pumps.yaml     ← its output. 24 tags. Do not edit.
```

## Try it

```sh
go run tools/tags.go     # 9 rows -> 24 tags
nautilus check           # schema, and both directions against the logic
nautilus test            # 4 acceptance tests
nautilus run             # dashboard and tag API on :8080
```

Then edit `tools/tags.go`:

- **add a pump** — one row in `pumps`, re-run, four new lines in the diff
- **add a derived tag to every pump** — one row in `perPump`, six new lines,
  every one the same shape and so trivially reviewable
- **typo an id** — `check` reports that no program binds it, instead of
  leaving a silent dead tag
- **flip `udtRole` to `"input"`** — the UDT tags stop being seeded. That is
  what a real EtherNet/IP project wants, and it is what makes a silent driver
  fault on the first read rather than hand the logic zeros that look like a
  healthy stopped pump.

Note that `P101` carries no `init:` and still comes up as a whole struct:

```json
"P101": { "Fault": false, "Hours": 0, "Running": false, "Speed": 0 }
```

That is zero-of-type seeding, from `type: Motor`. And `/api/meta` carries all
24 descriptions, composed in the expander — no `tag-meta:` block needed here,
because a config table *can* supply documentation. `tag-meta:` exists for
generators whose source cannot, which is the EtherNet/IP importer's problem
(Logix descriptions are not reachable over CIP browse).

## Five generators, one file

Nautilus never runs your generator. The only `exec` in the entire toolchain is
`git init` during `nautilus new` scaffolding — `check`, `run`, `test`, `build`,
and the LSP shell out to nothing, ever. Your generator is not a plugin, is not
registered, and is not configured. The interface is a committed YAML file that
satisfies the published JSON Schema.

So the language is entirely your business. To make that concrete rather than
merely stated, `tools/` holds the same generator five times:

| | run it with | notes |
|---|---|---|
| `tags.go` | `go run tools/tags.go` | the primary. Stdlib only |
| `tags.ts` | `node tools/tags.ts` | Node 22.6+ strips types natively — no package.json, no npm install, no tsc |
| `tags.deno.ts` | `deno run --allow-write tools/tags.deno.ts` | one static binary, and an explicit permission on the write |
| `tags.py` | `python3 tools/tags.py` | stdlib only — no pip, no venv |
| `pumps.csv` | `nautilus tags import-csv --name Tag --role Kind --type UDT --init Initial --unit EU --desc Service -o tags/pumps.yaml tools/pumps.csv` | different in kind: an integrator's export, already expanded, mapped by column |

All five emit an identical tag list, and `generators_test.go` asserts it on
every `go test ./...`, so the claim cannot rot. Go and the CSV importer are
required (both live in this repo); Node, Deno, and Python are skipped when
absent — CI must not fail for want of a runtime the project does not depend
on, which is the independence being demonstrated.

`tags.go` deliberately does **not** import `internal/tagfile`, even though
that package renders exactly this shape. A generator in your project could not
import it, so using it here would make the Go version a privileged peer rather
than an honest one. Its `render()` is the whole contract, in about thirty
lines: sort by name, keep whole floats decimal, quote text, omit empty fields.

**The cost of this independence, stated plainly:** because nautilus never runs
your generator, nothing verifies that `tags/pumps.yaml` still matches its
input. *Do not edit* is an honour-system header. Somebody can hand-patch the
file and every check still passes. The fix belongs in your project's CI, not
in nautilus — re-run the generator and `git diff --exit-code`. That is exactly
what `generators_test.go` does for this example.

## A sharp edge, since filed down

`P101.Running := x` when `P101` is a `VAR_EXTERNAL` used to parse, compile, and
then fault every single scan — visible only as `logicErrors` in `/api/state`,
because the VM had no store path for a field of a global. It works now: the
tag store holds the whole aggregate, so the VM reads it, writes the field, and
puts it back (`lang/ir/vm.go`, `writeLValue`).

`sim.st` still spells the older form out for all six pumps, and it is worth
keeping in your eye — copy, mutate the local, assign the whole struct back:

```
m := P101;
m.Running := P101_Enable AND NOT m.Fault;
P101 := m;
```

It is one tag write instead of three, so a struct you are rewriting field by
field is still cheaper assembled locally and assigned once. Both forms are
correct; pick by how many fields you touch.
