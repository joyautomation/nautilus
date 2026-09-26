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

See `../well-1/README.md` for the shared narrative and `../../README.md`
for the fleet story. This site is the one the "dark site" demo kills —
its `Well2__Online` companion on the SCADA host drops, `SitesOnline`
counts 2, and its alarms move to Suppressed instead of freezing lit.
