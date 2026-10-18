// "Copy Kit Component to Project…": the pure part — import rewriting, the
// header, the name rules.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { copyHeader, parseIndexExports, rewriteKitImports, validateNewComponentName, withHeader } from "./copyKitComponentLogic";

const INDEX = `
export { default as Tank } from './components/Tank.svelte';
export { default as TankGlyph } from './components/TankGlyph.svelte';
export { sparklineDomain, sparklineMetrics, sparklineGeometry } from './sparkline.js';
export { motion } from './motion.svelte.js';
export { tempColor } from './color.js';
export function inferPortDir(port) {}
export const PORTS = {};
`;

test("parseIndexExports: default-as, named, renamed and plain exports", () => {
  const ex = parseIndexExports(INDEX + "export { a as b, c } from './x.js';\n");
  for (const n of ["Tank", "TankGlyph", "sparklineMetrics", "motion", "tempColor", "inferPortDir", "PORTS", "b", "c"]) {
    assert.ok(ex.has(n), n);
  }
  assert.ok(!ex.has("a"), "the pre-rename name is not exported");
  assert.ok(!ex.has("default"));
});

test("rewriteKitImports: the kit's relative helper and component imports become package imports", () => {
  const ex = parseIndexExports(INDEX);
  const src = `<script lang="ts">
\timport { tempColor } from '../color.js';
\timport Glyph from './TankGlyph.svelte';
\timport TankGlyph from './TankGlyph.svelte';
\timport type { MimicPort } from '../mimic.js';
\tlet { levelPct = 50 } = $props();
</script>
<svg />`;
  const r = rewriteKitImports(src, ex);
  assert.deepEqual(r.unresolved, []);
  assert.ok(r.text.includes("\timport { tempColor } from '@joyautomation/nautilus-hmi';"));
  assert.ok(r.text.includes("\timport { TankGlyph as Glyph } from '@joyautomation/nautilus-hmi';"), r.text);
  assert.ok(r.text.includes("\timport { TankGlyph } from '@joyautomation/nautilus-hmi';"));
  assert.ok(r.text.includes("\timport type { MimicPort } from '@joyautomation/nautilus-hmi';"));
  assert.ok(!r.text.includes("'../"), "no relative import survives");
  assert.ok(r.text.includes("let { levelPct = 50 } = $props();"), "the rest is untouched");
});

test("rewriteKitImports: what the kit doesn't export stays and is reported", () => {
  const ex = parseIndexExports(INDEX);
  const src = `<script>
import { secret } from '../internal.js';
import * as geom from '../geom.js';
import '../style.css';
import { tempColor } from '../color.js';
</script>`;
  const r = rewriteKitImports(src, ex);
  assert.deepEqual(r.unresolved, [
    "import { secret } from '../internal.js';",
    "import * as geom from '../geom.js';",
    "import '../style.css';",
  ]);
  assert.ok(r.text.includes("import { secret } from '../internal.js';"));
  assert.ok(r.text.includes("import { tempColor } from '@joyautomation/nautilus-hmi';"));
});

test("rewriteKitImports leaves package imports alone", () => {
  const src = `<script>\nimport { onMount } from 'svelte';\nimport { Tank } from '@joyautomation/nautilus-hmi';\n</script>`;
  const r = rewriteKitImports(src, new Set());
  assert.equal(r.text, src);
  assert.deepEqual(r.unresolved, []);
});

test("withHeader: into the first script block, or a new one", () => {
  const h = copyHeader("Tank", "MyTank", "0.7.1");
  assert.ok(h.startsWith("// MyTank: Tank.svelte copied from @joyautomation/nautilus-hmi@0.7.1"));
  assert.ok(h.includes("<Mimic registry={{ MyTank }}>"));
  const withScript = withHeader(`<script lang="ts">\n\tlet x = 1;\n</script>\n<svg />`, h);
  const tabbed = h
    .split("\n")
    .map((l) => "\t" + l)
    .join("\n");
  assert.ok(withScript.startsWith(`<script lang="ts">\n${tabbed}\n\tlet x = 1;`), "indented like the line after it");
  const bare = withHeader(`<svg />`, h);
  assert.equal(bare, `<script lang="ts">\n${h}\n</script>\n\n<svg />`);
});

test("validateNewComponentName: PascalCase, not a built-in, not taken", () => {
  const v = (n: string) => validateNewComponentName(n, ["Tank", "Pump"], ["MyPump"]);
  assert.equal(v("MyTank"), null);
  assert.equal(v("Tank2"), null);
  assert.match(v("tank") ?? "", /PascalCase/);
  assert.match(v("my-tank") ?? "", /PascalCase/);
  assert.match(v("Tank") ?? "", /built-in/);
  assert.match(v("MyPump") ?? "", /already exists/);
});

test("rewriteKitImports: a multi-line import with inline type specifiers", () => {
  const ex = parseIndexExports("export { STATUS_META, valueStatus } from './quality.js';\n");
  const src = `<script lang="ts">
\timport {
\t\tSTATUS_META,
\t\ttype ValueStatus
\t} from '../quality.js';
</script>`;
  const r = rewriteKitImports(src, ex);
  assert.deepEqual(r.unresolved, []);
  assert.ok(r.text.includes("\timport { STATUS_META, type ValueStatus } from '@joyautomation/nautilus-hmi';"), r.text);
});
