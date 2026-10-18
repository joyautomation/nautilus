// The component preview's pure part: which Svelte buffers it compiles from.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { previewSources, previewTargetForPath } from "./componentPreviewLogic";

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
