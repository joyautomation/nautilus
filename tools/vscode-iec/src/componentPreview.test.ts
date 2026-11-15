// The component preview's pure part: which Svelte buffers it compiles from.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { declaredProps, isScenePart, sceneHostPath, sceneHostSource, previewSources, previewTargetForPath, propsWithoutDefault } from "./componentPreviewLogic";

const doc = (fsPath: string, isDirty: boolean, text: string, isSvelte = fsPath.endsWith(".svelte")) => ({
  fsPath,
  isSvelte,
  isDirty,
  text: () => text,
});

test("previewSources takes the previewed file always, other .svelte buffers only while dirty", () => {
  const docs = [
    doc("/p/Tank.svelte", false, "tank"),
    doc("/p/Parts.svelte", true, "parts (dirty)"),
    doc("/p/Other.svelte", false, "other (clean)"),
    doc("/p/notes.md", true, "md"),
  ];
  assert.deepEqual(previewSources(docs, "/p/Tank.svelte"), {
    "/p/Tank.svelte": "tank",
    "/p/Parts.svelte": "parts (dirty)",
  });
});

test("previewSources is empty when nothing is open", () => {
  assert.deepEqual(previewSources([], "/p/Tank.svelte"), {});
});

test("previewTargetForPath: a .svelte previews itself, a sidecar its component", () => {
  const bySvelte = (p: string) => (p.endsWith(".svelte") && !p.includes("+") ? p.split("/").pop()!.replace(".svelte", "") : null);
  const byFile = (b: string) => (/^(.+)\.component\.json$/.exec(b)?.[1] ?? null);
  const t = (p: string) => previewTargetForPath(p, ["Tank", "Pump"], bySvelte, byFile);
  assert.deepEqual(t("/hmi/src/lib/Dial.svelte"), { kind: "svelte", name: "Dial" });
  assert.deepEqual(t("/hmi/src/lib/Dial.component.json"), { kind: "custom", name: "Dial" });
  assert.deepEqual(t("/hmi/src/lib/Tank.component.json"), { kind: "builtin", name: "Tank" });
  assert.deepEqual(t("C:\\hmi\\src\\lib\\Pump.component.json"), { kind: "builtin", name: "Pump" });
  assert.equal(t("/hmi/src/routes/+page.svelte"), null);
  assert.equal(t("/hmi/package.json"), null);
});

test("propsWithoutDefault names the $props() entries with no default", () => {
  const src = (pattern: string, type = "") => `<script lang="ts">\n  let ${pattern}${type} = $props();\n</script>\n<g />`;
  assert.deepEqual(
    propsWithoutDefault(src("{ x, y, tag, status, title = '' }", ": { x: number; y: number; tag: string; status: ValveState; title?: string }")),
    ["x", "y", "tag", "status"]
  );
  // a default holding commas, braces, an arrow or a comparison is still one default
  assert.deepEqual(
    propsWithoutDefault(src("{ a = { p: 1, q: [2, 3] }, b = (v) => v >= 2, c = 'x, y', d, class: klass, ...rest }")),
    ["d", "class"]
  );
  assert.deepEqual(propsWithoutDefault(src("{ label = '', width = 26 }")), []);
  // optional in the inline type, or the children snippet: not needed to draw
  assert.deepEqual(
    propsWithoutDefault(
      src(
        "{ value, height, label, children, onclick }",
        ": {\n    value: number;\n    height?: number;\n    readonly label?: string\n    children?: Snippet;\n    onclick?: (e: MouseEvent) => void;\n  }"
      )
    ),
    ["value"]
  );
  // a named type says nothing here: every undefaulted name is listed
  assert.deepEqual(propsWithoutDefault(src("{ value, height }", ": Props")), ["value", "height"]);
  // an earlier destructure is not the props pattern
  assert.deepEqual(propsWithoutDefault("<script>\n  const { a } = thing;\n  let { b } = $props();\n</script>"), ["b"]);
  // no destructure to read, or no $props() at all
  assert.deepEqual(propsWithoutDefault("<script>\n  let props = $props();\n</script>"), []);
  assert.deepEqual(propsWithoutDefault("<svg />"), []);
});

test("declaredProps lists every $props() entry and whether it must be given", () => {
  const src = `<script lang="ts">\n  let { selected = $bindable(null), tag, children, ...rest }: { selected?: string | null; tag: string } = $props();\n</script>`;
  assert.deepEqual(declaredProps(src), [
    { name: "selected", needed: false },
    { name: "tag", needed: true },
  ]);
  assert.deepEqual(declaredProps("<p>no props</p>"), []);
});

test("isScenePart: imports from Threlte without its own Canvas", () => {
  const s = (imports: string) => `<script lang="ts">\n${imports}\n</script>\n<T.Mesh />`;
  assert.equal(isScenePart(s("\timport { T, useTask } from '@threlte/core';\n\timport { BufferGeometry } from 'three';")), true);
  assert.equal(isScenePart(s('import { OrbitControls } from "@threlte/extras";')), true);
  // brings its own canvas: an ordinary component that happens to draw in 3D
  assert.equal(isScenePart(s("import { Canvas, T } from '@threlte/core';")), false);
  assert.equal(
    isScenePart(s("import { T } from '@threlte/core';\nimport { SceneView, LOOKS } from '@joyautomation/nautilus-hmi-3d';")),
    false
  );
  // a mention is not an import, and a type import runs nothing
  assert.equal(isScenePart(s("// like '@threlte/core' but not\nimport { fly } from 'svelte/transition';")), false);
  assert.equal(isScenePart(s("import type { ThrelteContext } from '@threlte/core';")), false);
  assert.equal(isScenePart(s("import { Vector3 } from 'three';")), false);
});

test("a scene part's host sits beside it and imports it", () => {
  assert.equal(sceneHostPath("/p/hmi/src/lib/Rain.svelte"), "/p/hmi/src/lib/Rain.nx-scene-host.svelte");
  const host = sceneHostSource("/p/hmi/src/lib/Rain.svelte");
  assert.match(host, /import Part from "\/p\/hmi\/src\/lib\/Rain\.svelte";/);
  assert.match(host, /<Canvas /);
  // itself a component with a canvas, so never taken for a part to host again
  assert.equal(isScenePart(host), false);
});
