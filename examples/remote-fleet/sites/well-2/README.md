# well-2 — a second, standalone Sparkplug B edge node

Same pattern as `../well-1` — one pump, hysteresis, a run-hours meter —
deliberately **not** a shared library: a second copy of the pattern with
its own setpoints and well geometry (narrower hysteresis band, a smaller
pump, a smaller wet well). Each site in this fleet deploys alone.
Publishes to the `Water` Sparkplug group as edge node `Well2`.

```sh
naut check .    # validate offline
naut test  .    # the acceptance suite, virtual time
naut run   .    # dashboard + tag API on http://localhost:8092
```

**Needs naut ≥ 0.12.0.** Runs clean on the released CLI — see
`../../README.md` for the fleet-wide version check.

## What to open first

- `well.st` — the same hysteresis/speed-clamp shape as `../well-1`, its
  own setpoints and geometry.
- `well-2_test.yaml` — the acceptance suite.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| Sparkplug B edge node: a second, standalone node in the same group | `nautilus.yaml` `sparkplug:` | [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/) |
| Dark-site suppression, from the site that gets killed in the fleet demo | `../../README.md`'s story beat 3 | [Sparkplug host](https://nautilus.joyautomation.com/guides/sparkplug-host/) |
| Acceptance tests: virtual time, `suspend:` | `well-2_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |

See `../well-1/README.md` for the shared narrative and `../../README.md`
for the fleet story. This site is the one the "dark site" demo kills —
its `Well2__Online` companion on the SCADA host drops, `SitesOnline`
counts 2, and its alarms move to Suppressed instead of freezing lit.
