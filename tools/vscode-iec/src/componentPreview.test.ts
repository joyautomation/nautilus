// The component preview's pure part: which Svelte buffers it compiles from.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { previewSources } from "./componentPreviewLogic";

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
