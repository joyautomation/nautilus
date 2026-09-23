# Agent brief: fix the Sparkplug edge-node findings

Read `docs/handover/2026-09-19-sparkplug-edge-findings.md` in full first. It documents three findings in the
Sparkplug edge node (`sparkplug/`), produced by an integration suite in a sibling repo
(`~/Development/joyautomation/ignition`). The root cause of item 1 is already confirmed with a goroutine dump, so
don't re-diagnose it. Verify it, fix it, and prove the fix.

Do item 1 first and completely; it is a high-severity bug. Then items 2 and 3 as separate commits, each of which
can be taken or left independently.

## Item 1: the publish loop wedges forever after a keepalive timeout

1. **Reproduce first.** Run `scripts/repro-sparkplug-silent-link.sh` (about 70 s; it needs a broker on
   `localhost:1883`, or use the `MOSQ=` form in its header to borrow the ignition dev stack's broker). Confirm it
   FAILs and that the goroutine dump shows `scanAndPublish` parked at `sparkplug/data.go:59`. **It is a race**: it
   fired 6 times out of 6 and then missed once, so if your first run passes, run it again before concluding
   anything.
2. **Write a failing unit test before the fix.** The invariant: *one publish whose token never completes must not
   stop the next tick.* Stopping an in-process broker closes sockets and does **not** reproduce this. Use a fake
   `mqtt.Client`, or a proxy that stops forwarding without closing. The test must fail on the current code for the
   right reason, and must not take 45 s.
3. **Fix every unbounded token wait** listed in the handover, not only `data.go:59`. Follow the pattern
   `sparkplug/host` already uses (`WaitTimeout` with a named timeout); don't invent a second convention.
4. **Handle what the handover lists under "What a fix needs":**
   - `seq` must not advance for messages that were not sent.
   - Don't attempt to publish while the client isn't connected (`born` lags the connection state, which is the
     window that causes this).
   - Decide deliberately what happens to a DATA tick that times out when `store-forward` is enabled. State the
     decision and the reason in the commit message.
5. **Prove it:**
   - the new unit test passes;
   - `go test -race ./sparkplug/...` passes;
   - `scripts/repro-sparkplug-silent-link.sh` exits 0 **five times in a row** (one pass proves nothing, see the
     handover: the unbounded wait only wedges when a publish is in flight at the instant of wake-up). The unit
     test, which forces the never-completing token deterministically, is the real proof;
   - if Docker is available, run the sibling suite:
     `cd ../ignition && scripts/dev-up.sh && cd integration && go test -run TestASilentNode -v ./...`.
     That test currently **skips** its last step with a message naming this bug. With the fix it should reach the
     end and pass. If it does, remove the guard in `../ignition/integration/mantle/mantle_test.go` (search for
     `KNOWN, AND NOT MANTLE'S`) and say so. Change nothing else in that repo.

## Item 2: a manifest can't declare an integer scalar tag

`internal/project` `normalize()`. Prefer the option that needs no manifest changes: resolve a scalar tag's type from
the `VAR_EXTERNAL` declarations of the programs that use it. Check what breaks: existing examples and acceptance
tests seed REAL tags with integer literals like `init: 65` and must keep working. Add tests for a `DINT` tag
birthing as Int64, and for a REAL tag with an integer literal staying a Double.

## Item 3: publish `unit` / `desc` as Sparkplug metric properties

Keys `engUnit` and `documentation`, in N/DBIRTH only, including template members. The handover notes this repo's
own host-side decoder drops properties; make the round trip work. Check the TCK edge-node profile still passes if
you can run it.

## Working rules

- The checkout is on branch `diff-revisions`, with an untracked `examples/.vscode` that belongs to the user: leave
  it alone. **Ask which branch to work from before committing anything.** Don't assume.
- `HANDOFF.md` says CI only runs on `main` and on PRs. Don't push or open a PR without asking.
- Read `HANDOFF.md` and the `sparkplug` package's existing tests for conventions before writing code. Match the
  surrounding style and comment density.
- Other `naut` processes running on this machine belong to the user. **Never kill processes by name**; only
  ones you started, by PID.
- Report honestly: if a test can't be made to fail before the fix, or the repro still fails after it, say so with
  the output rather than adjusting the test until it passes.
- When done, add an **Outcome** section to `docs/handover/2026-09-19-sparkplug-edge-findings.md`: what changed,
  what was verified and how, and anything left open.
