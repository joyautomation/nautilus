// Eject to component: the source the kind editor writes, the contract it
// carries, and the scene re-pointed at it.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { componentNameFor, ejectSource, kindMembersOf, repointScene } from "./kindEject";
import type { KindDoc } from "./kindOps";

const pump: KindDoc = {
  type: "Motor",
  model: "models/submersible-pump.glb",
  labelAt: [0, 1.5, 0],
  status: "{Running?run:stopped} {SpeedHz:0} Hz",
  ports: { discharge: { at: { part: "Flange", anchor: "top" } } },
  drive: [
    { mesh: "Motor", tint: { bind: "Running", on: "running" } },
    { mesh: "Impeller", spin: { axis: "y", revPerS: { bind: "SpeedHz", scale: 0.0333 } } },
  ],
};

test("componentNameFor: PascalCase from the kind's name", () => {
  assert.equal(componentNameFor("submersible-pump"), "SubmersiblePump");
  assert.equal(componentNameFor("wet_well 2"), "WetWell2");
  assert.equal(componentNameFor("3way"), "Kind3way");
});

test("kindMembersOf: members, then the drives' and the status template's, once each", () => {
  assert.deepEqual(kindMembersOf(pump), ["Running", "SpeedHz"]);
  assert.deepEqual(kindMembersOf({ members: ["Fault"], drive: [{ mesh: "A", visible: { bind: "!Fault" } }], status: "{Level:1}" }), ["Fault", "Level"]);
});

test("ejectSource: a module script with the contract, an instance script with the model and drives, a <ModelNode>", () => {
  const src = ejectSource({ name: "submersible-pump", file: "submersible-pump.kind.json", kind: pump, measured: { size: [0.6, 1.9, 1.2], center: [0, 0.95, -0.15] }, date: "2026-10-24" });
  assert.ok(src.startsWith('<script lang="ts" module>'), src);
  assert.ok(src.includes("ejected from submersible-pump.kind.json on 2026-10-24"), src);
  assert.ok(src.includes("type: 'Motor'"), src);
  assert.ok(src.includes("members: ['Running', 'SpeedHz']"), src);
  assert.ok(src.includes("bounds: { size: [0.6, 1.9, 1.2], center: [0, 0.95, -0.15] }"), "measured bounds stand in for auto: " + src);
  assert.ok(src.includes("labelAt: [0, 1.5, 0]"), src);
  assert.ok(src.includes("ports: { discharge: { at: { part: 'Flange', anchor: 'top' } } }"), src);
  assert.ok(src.includes("status: (value, good, over) => formatStatus('{Running?run:stopped} {SpeedHz:0} Hz', value, good, over)"), src);
  assert.ok(src.includes("const model = 'models/submersible-pump.glb';"), src);
  assert.ok(src.includes("{ mesh: 'Motor', tint: { bind: 'Running', on: 'running' } }"), src);
  assert.ok(src.includes("{ mesh: 'Impeller', spin: { axis: 'y', revPerS: { bind: 'SpeedHz', scale: 0.0333 } } }"), src);
  assert.ok(src.includes("<ModelNode data={{ model, drive }} {value} {good} {label} {selected} {...over} />"), src);
  // Declared bounds win over the measured box; a kind with nothing has an empty contract.
  const declared = ejectSource({ name: "x", file: "x.kind.json", kind: { model: "m.glb", bounds: { size: [1, 1, 1], center: [0, 0.5, 0] } }, measured: { size: [2, 2, 2], center: [0, 1, 0] }, date: "d" });
  assert.ok(declared.includes("bounds: { size: [1, 1, 1], center: [0, 0.5, 0] }"), declared);
  assert.ok(declared.includes("const drive: Drive[] = [];"), declared);
});

test("repointScene: the entry naming the file becomes a component kind with the contract; others untouched", () => {
  const scene = JSON.stringify({
    kinds: { "submersible-pump": "hmi/src/lib/kinds/submersible-pump.kind.json", "wet-well": "hmi/src/lib/kinds/wet-well.kind.json" },
    nodes: [{ id: "P101", kind: "submersible-pump", pos: [0, 0, 0] }],
  });
  const out = repointScene(scene, "/proj", "/proj/hmi/src/lib/kinds/submersible-pump.kind.json", "/proj/hmi/src/lib/kinds/SubmersiblePump.svelte", pump);
  assert.ok(out, "changed");
  const doc = JSON.parse(out!);
  assert.deepEqual(doc.kinds["submersible-pump"], { component: "hmi/src/lib/kinds/SubmersiblePump.svelte", type: "Motor", members: ["Running", "SpeedHz"] });
  assert.equal(doc.kinds["wet-well"], "hmi/src/lib/kinds/wet-well.kind.json");
  assert.equal(repointScene(scene, "/proj", "/proj/hmi/src/lib/kinds/other.kind.json", "/proj/x.svelte", pump), undefined);
});
