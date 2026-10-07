# Runtime claims inventory

One row per behaviour the documentation promises about the nautilus
runtime, languages and drivers, with the tests that prove it. The editor
has its own inventory (`tools/vscode-iec/webview-ui/gesture-harness/INVENTORY.md`);
this one covers everything that is not a gesture.

The docs site's `/verified/runtime/` pages are built from these files
joined to the `go test -json` output of the last green CI run on `main`,
so every claim shows the verdict of the tests it names. `go run
./tools/evidence check` (run in CI) fails when a claim names a test that
does not exist, so a rename cannot silently orphan a claim.

## Layout

One file per documentation page, named after it:

```
docs/claims/<page>.yaml      e.g. modbus.yaml for guides/modbus.md,
                             functions.yaml for docs/functions.md
```

```yaml
page: website/src/content/docs/guides/modbus.md   # the page the claims come from
title: Modbus                                       # how the page is listed
prefix: MB                                          # id prefix, unique across files
claims:
  - id: MB-001
    claim: >-
      A coil write from a Modbus master lands in the bound tag before the
      next scan reads it.
    source: "#writing-from-the-master"     # anchor on the page (or another page path + anchor)
    tests:
      - go ./modbus TestSlaveCoilWriteReachesTag
      - go ./lang/conformance TestConformance/fb-ton/output follows input after PT
      - naut examples/lift-station pump seals in below the start level
    partial: false          # optional; true when the tests cover only part of the claim
    note: ""                # optional; what is or is not covered, why it is a gap
```

### Fields

- **id**: `<prefix>-NNN`, numbered in page order, stable once written.
  Retire an id rather than reuse it.
- **claim**: one behavioural promise, written as a plain statement a user
  could rely on and a test could falsify: semantics, limits, error
  behaviour, protocol behaviour, timing, persistence. Not marketing, not
  how-to steps, not editor gestures. Merge sentences that make the same
  promise; split a sentence that makes two.
- **source**: the heading anchor the claim is made under (Starlight slugs:
  lowercase, spaces → `-`, punctuation dropped).
- **tests**: zero or more test references, each on one line:
  - `go <package dir> <TestName>[/<subtest>...]` — a Go test. The package
    is the directory relative to the repo root, with `./`. A reference
    covers that test and every subtest beneath it. Subtest names are
    written as they appear in `t.Run` (or in a `*_test.yaml` for the
    conformance corpus); spaces are matched against go's `_` rewriting.
    The conformance corpus runs one subtest per feature and one per YAML
    test: `go ./lang/conformance TestConformance/<feature>/<yaml test name>`,
    or the whole feature with `go ./lang/conformance TestConformance/<feature>`.
  - `naut <project root> <test name>` — a test in an example project's
    `*_test.yaml`, which CI runs with `naut test -json`.
- **partial** / **note**: say plainly what is not covered.

An empty `tests: []` is a **gap**, and gaps are the point: they are listed
on the site as "no test yet" rather than left out. Do not stretch a test
to cover a claim it does not assert.

### When a test covers a claim

A claim is fully covered (`partial: false`) only when all of these hold.
An audit of the first draft found 40% of "verified" claims failing one of
them:

1. **It would fail if the feature broke.** Read the test body and ask:
   if the promised behaviour were removed or wrong, would this test go
   red? A test that exercises the path but cannot tell working from broken
   is not coverage. Examples: an `always:` that never sees a transient
   violation can't tell every-scan from end-of-step checking; a load test
   whose program overwrites the loaded value every scan can't see a wrong
   load; a fallback test that passes whether or not the fallback ran.
2. **Every clause is asserted.** A claim joining promises with "and" or
   "or" (DIV *and* MOD, TIME *and* LTIME, by name *or* by number, `lib/`
   *or* the project root) is covered only when each clause is. Otherwise
   `partial: true` and the note names the unasserted clause.
3. **The page says it.** The claim states what the page promises, in the
   page's terms. Detail taken from the code or a test (status codes,
   message strings, extra cases) is not a claim of the page; if the page
   quotes a string, check the code produces that string.
4. **Every listed test earns its place.** A test about something adjacent
   (a different diagnostic, an editor gesture) does not go in the list.

## Which tests count

Any Go test or acceptance test that runs in CI on `main`. Some only run in
a dedicated CI job with a peer present (the Sparkplug TCK, the pymodbus /
snmpsim / Redfish foreign-stack tests); they still count — the evidence
merges every job's results. Tests that only run against hardware (a Logix
controller, a real BMC) skip in CI; a claim resting only on those is
listed with `partial: true` and a note saying it is verified on hardware
only.
