import { strict as assert } from "node:assert";
import { test } from "node:test";
import { kindNameFromFilename, locateKindModel, resolveKindModel } from "./kindEditorLogic";

const P = "/w/proj";
const have = new Set([`${P}/hmi/static/models/pump.glb`, `${P}/hmi/src/lib/kinds/local.glb`]);
const exists = (f: string) => have.has(f);

test("kindNameFromFilename strips .kind.json", () => {
  assert.equal(kindNameFromFilename("/w/proj/hmi/src/lib/kinds/submersible-pump.kind.json"), "submersible-pump");
  assert.equal(kindNameFromFilename("Pump.json"), "Pump");
});

test("locateKindModel walks up from the kind file's folder to the app's static/", () => {
  assert.equal(locateKindModel("models/pump.glb", `${P}/hmi/src/lib/kinds`, exists), `${P}/hmi/static/models/pump.glb`);
  assert.equal(locateKindModel("/models/pump.glb", `${P}/hmi/src/lib/kinds`, exists), `${P}/hmi/static/models/pump.glb`);
  // Next to the file wins first.
  assert.equal(locateKindModel("local.glb", `${P}/hmi/src/lib/kinds`, exists), `${P}/hmi/src/lib/kinds/local.glb`);
  assert.equal(locateKindModel("models/none.glb", `${P}/hmi/src/lib/kinds`, exists), undefined);
  assert.equal(locateKindModel("../pump.glb", `${P}/hmi/src/lib/kinds`, exists), undefined);
});

test("resolveKindModel swaps the path for a URI and reports where it was", () => {
  const r = resolveKindModel({ model: "models/pump.glb", status: "x" }, `${P}/hmi/src/lib/kinds`, exists, (f) => `vscode-resource:${f}`);
  assert.equal(r.kind.model, `vscode-resource:${P}/hmi/static/models/pump.glb`);
  assert.equal(r.kind.status, "x");
  assert.deepEqual(r.model, { pointer: "/model", path: "models/pump.glb", file: `${P}/hmi/static/models/pump.glb` });
  const miss = resolveKindModel({ model: "models/none.glb" }, `${P}/hmi/src/lib/kinds`, exists, (f) => f);
  assert.equal(miss.kind.model, "models/none.glb");
  assert.deepEqual(miss.model, { pointer: "/model", path: "models/none.glb" });
  assert.deepEqual(resolveKindModel({ type: "Motor" }, P, exists, (f) => f), { kind: { type: "Motor" } });
});
