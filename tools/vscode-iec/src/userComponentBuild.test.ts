// Tests for the user-components build harness. `customComponentNames` is
// pure and covered directly; `buildUserComponentsBundle` is exercised for
// real against a worked example (testdata/user-components/Supply.svelte,
// cut from the hmi-demo app's own component library) — it compiles with
// this package's OWN node_modules (the `svelte` devDependency declared for
// exactly this purpose, plus esbuild's TS transpile of its
// `<script lang="ts">`), proving the harness works against a real,
// unmodified user component, not a fixture built to fit the compiler.
//
// Supply.svelte (rather than HeatExchanger.svelte, the fixture's original
// worked example) is used here because it has no bare imports beyond
// `svelte` itself — HeatExchanger imports `@joyautomation/nautilus-hmi`,
// which would need that package built and resolvable from this fixture's
// node_modules too, and wiring that in is out of scope for a fixture move.
// Both .svelte files still live in testdata/user-components/ for future use.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import { buildUserComponentsBundle, customComponentNames } from "./userComponentBuild";

test("customComponentNames excludes built-ins, dedupes, and sorts", () => {
  const builtins = new Set(["Tank", "Pump", "Valve", "Gauge", "Sparkline"]);
  assert.deepEqual(
    customComponentNames(["Tank", "HeatExchanger", "Pump", "HeatExchanger", "Widget"], builtins),
    ["HeatExchanger", "Widget"]
  );
  assert.deepEqual(customComponentNames(["Tank", "Pump"], builtins), []);
  assert.deepEqual(customComponentNames([], builtins), []);
});

test("customComponentNames ignores empty/falsy names", () => {
  assert.deepEqual(customComponentNames(["", "Foo"], new Set()), ["Foo"]);
});

const SUPPLY = path.join(__dirname, "..", "src", "testdata", "user-components", "Supply.svelte");
const HAS_FIXTURE = fs.existsSync(SUPPLY) && fs.existsSync(path.join(__dirname, "..", "node_modules", "svelte"));

test(
  "buildUserComponentsBundle compiles the Supply component with this package's svelte, TS script and all",
  { skip: !HAS_FIXTURE ? "testdata/user-components fixture missing, or svelte isn't npm-installed here" : false },
  async () => {
    const outfile = path.join(await fs.promises.mkdtemp(path.join(os.tmpdir(), "nx-user-components-")), "user-components.js");
    const result = await buildUserComponentsBundle({ Supply: SUPPLY }, outfile);
    assert.deepEqual(result.diagnostics, []);
    assert.deepEqual(result.built, ["Supply"]);
    const code = await fs.promises.readFile(outfile, "utf8");
    assert.match(code, /__NX_USER_COMPONENTS__/);
    assert.match(code, /Supply/);
    // The compiled output is plain JS — no TypeScript syntax should have
    // survived the <script lang="ts"> preprocessing step.
    assert.doesNotMatch(code, /: \s*\{\s*tempC\?: number/);
  }
);

test("buildUserComponentsBundle still emits a bundle when a component is missing entirely", async () => {
  const outfile = path.join(await fs.promises.mkdtemp(path.join(os.tmpdir(), "nx-user-components-")), "user-components.js");
  const result = await buildUserComponentsBundle({ Nope: path.join(os.tmpdir(), "does-not-exist", "Nope.svelte") }, outfile);
  assert.equal(result.built.length, 0);
  assert.equal(result.diagnostics.length, 1);
  assert.equal(result.diagnostics[0].component, "Nope");
  const code = await fs.promises.readFile(outfile, "utf8");
  assert.match(code, /__NX_USER_COMPONENTS__/);
});

test(
  "buildUserComponentsBundle compiles from `sources` text in place of the disk file",
  { skip: !HAS_FIXTURE ? "testdata/user-components fixture missing, or svelte isn't npm-installed here" : false },
  async () => {
    const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "nx-user-components-"));
    const outfile = path.join(dir, "user-components.js");
    // The buffer's text, not the disk's: a marker that is nowhere in Supply.svelte.
    const marker = "nx-preview-buffer-text-" + Date.now();
    const text = `<script lang="ts">\n\tlet { label = '' }: { label?: string } = $props();\n</script>\n<div class="x">${marker} {label}</div>\n`;
    const result = await buildUserComponentsBundle({ Supply: SUPPLY }, outfile, { sources: { [SUPPLY]: text } });
    assert.deepEqual(result.diagnostics, []);
    assert.deepEqual(result.built, ["Supply"]);
    const code = await fs.promises.readFile(outfile, "utf8");
    assert.match(code, new RegExp(marker));
    assert.doesNotMatch(code, /tempC/); // nothing of the disk file made it in
  }
);

test(
  "buildUserComponentsBundle reports a syntax error in `sources` text as a diagnostic and builds nothing",
  { skip: !HAS_FIXTURE ? "testdata/user-components fixture missing, or svelte isn't npm-installed here" : false },
  async () => {
    const dir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "nx-user-components-"));
    const outfile = path.join(dir, "user-components.js");
    const result = await buildUserComponentsBundle(
      { Supply: SUPPLY },
      outfile,
      { sources: { [SUPPLY]: "<script>\n\tlet x = \n</script>\n<div>{x</div>\n" } }
    );
    assert.deepEqual(result.built, []);
    assert.equal(result.diagnostics.length, 1);
    assert.equal(result.diagnostics[0].component, "Supply");
    assert.ok(result.diagnostics[0].message.length > 0);
  }
);
