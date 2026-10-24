# tools/blackbox: the tester's checks, run here

The independent tester (the `nautilus-blackbox` sibling repository) writes
checks: scripts a stranger can run against a fresh `naut` with a foreign
client and get pass or fail. This directory holds the ones imported into
this repo, byte for byte, and a runner that executes them as Go subtests
so their verdicts join the runtime evidence (`docs/design/blackbox.md`).

## Run them

```sh
python3 -m venv .venv && .venv/bin/pip install -r tools/blackbox/requirements.txt
NAUTILUS_BLACKBOX_PYTHON=$PWD/.venv/bin/python go test ./tools/blackbox -run TestBlackbox -v
```

Without `NAUTILUS_BLACKBOX_PYTHON` the test skips. Other knobs:

| env | default | meaning |
|---|---|---|
| `NAUTILUS_BLACKBOX_PYTHON` | — | the interpreter with the oracle clients installed; required |
| `NAUTILUS_BLACKBOX_NAUT` | built from this checkout | the binary under test (a release's, in blackbox-release.yml) |
| `NAUTILUS_BLACKBOX_DISPOSITION` | `gate` | which disposition to run: `gate` or `release` |

Checks run one project at a time: the runner copies the project directory
to a temp dir, starts `naut run` there, waits for the HTTP API, and runs
every check that names that project. A project's addresses are the ones
its `nautilus.yaml` declares, so two projects wanting the same port is
fine (they never run at once) and a check never hardcodes one.

## The contract a check meets

Written by the tester, copied here unchanged (`runs/README.md` in the
blackbox repo is the original):

- `python -I <script>` with the oracle venv; exit `0` passes, anything
  else fails; stdout is the evidence and is kept.
- Addresses from the environment: `SUT_HTTP`, `SUT_OPCUA`, `SUT_MODBUS`,
  `SUT_MQTT` as the project declares them, `SUT_PROJECT` for the running
  project's directory, plus anything the manifest entry's `env` adds.
- The check sets up its own state through customer-facing interfaces.
- `gate` checks are deterministic and finish in seconds.

## The manifest

`checks.yaml`:

```yaml
oracles:                 # pinned in requirements.txt; the run's REPORT records what it used
  asyncua: 2.1.0
checks:
  - id: 001-F08-server-object-read-only   # the tester's id, verbatim; also the subtest name
    run: 2026-10-22-001                    # the run folder in the blackbox repo
    finding: F-08                          # or pass: <what passed>
    disposition: gate                      # gate | release | soak | manual | once
    surface: opcua
    oracle: asyncua + OPC UA Part 5
    project: projects/2026-10-22-001/typed # copied from the run's evidence/
    script: checks/2026-10-22-001/001-F08-server-object-read-only.py
    env: {}
    note: ""                               # a changed disposition and why; a reason it is not run
```

Only `gate` and `release` entries have scripts the runner executes. The
others are listed so the manifest is the full record of what the tester
proposed.

## Importing a run

1. For each entry in the run's `checks.yaml` with disposition `gate` or
   `release`: copy the script to `checks/<run>/`, the project directory to
   `projects/<run>/`, and the entry here with `run:` added and paths
   rewritten. Nothing else changes; `diff` against the run folder must be
   empty for the script and the project files.
2. Copy the other entries too, without scripts.
3. Pin any new oracle in `requirements.txt` at the version the run's
   REPORT records.
4. Run the gate locally. A check that fails against the fix commit is a
   finding not yet fixed, or a check that depends on something the runner
   does not provide: say which in `note`, do not edit the check.
5. Cite the check from the claims it proves:
   `go ./tools/blackbox TestBlackbox/<id>` in `docs/claims/<page>.yaml`.
