// Reducer tests for the kind file: ops against real text, asserting on the
// canonical result. Run with `npm test` (node --test over out/).

import { strict as assert } from "node:assert";
import { test } from "node:test";
import { applyKindOp, driveError, formatKind, parseKind, type Drive, type KindOp } from "./kindOps";

const apply = (text: string, op: KindOp): string => {
  const res = applyKindOp(text, op);
  assert.ok("text" in res, `expected success, got: ${(res as { error: string }).error}`);
  return (res as { text: string }).text;
};
const refuse = (text: string, op: KindOp): string => {
  const res = applyKindOp(text, op);
  assert.ok("error" in res, "expected a refusal");
  return (res as { error: string }).error;
};

const spin: Drive = { mesh: "Impeller", spin: { axis: "y", revPerS: { bind: "SpeedHz", scale: 0.0333 } } };
const tint: Drive = { mesh: "Motor", tint: { bind: "Running", on: "running" } };

const BASE = formatKind({
  type: "Motor",
  model: "models/submersible-pump.glb",
  status: "{Running?run:stopped} {SpeedHz:0} Hz",
  labelAt: [0, 1.5, 0],
  drive: [tint, spin],
});

test("formatKind: fixed key order, vectors and drives on one line each", () => {
  assert.equal(
    BASE,
    `{
  "type": "Motor",
  "model": "models/submersible-pump.glb",
  "labelAt": [0, 1.5, 0],
  "status": "{Running?run:stopped} {SpeedHz:0} Hz",
  "drive": [
    { "mesh": "Motor", "tint": { "bind": "Running", "on": "running" } },
    { "mesh": "Impeller", "spin": { "axis": "y", "revPerS": { "bind": "SpeedHz", "scale": 0.0333 } } }
  ]
}
`
  );
  // Idempotent, and a hand-written file with other key orders normalizes to it.
  assert.equal(formatKind(parseKind(BASE)), BASE);
  // Drive ORDER is kept (a mesh named twice gets both, in order); key order inside is normalized.
  const scrambled = JSON.stringify({ drive: [{ tint: { on: "running", bind: "Running" }, mesh: "Motor" }, { spin: { revPerS: { scale: 0.0333, bind: "SpeedHz" }, axis: "y" }, mesh: "Impeller" }], status: "{Running?run:stopped} {SpeedHz:0} Hz", labelAt: [0, 1.5, 0], model: "models/submersible-pump.glb", type: "Motor" });
  assert.equal(formatKind(parseKind(scrambled)), BASE);
});

test("formatKind: empty lists go, bounds keeps size then center, {} is a bare contract", () => {
  assert.equal(formatKind({ members: [], drive: [] }), "{}\n");
  assert.equal(formatKind({ bounds: { center: [0, 0.2, 0], size: [0.4, 0.4, 0.4] } }), '{\n  "bounds": { "size": [0.4, 0.4, 0.4], "center": [0, 0.2, 0] }\n}\n'.replace('{ "size": [0.4, 0.4, 0.4], "center": [0, 0.2, 0] }', '{\n    "size": [0.4, 0.4, 0.4],\n    "center": [0, 0.2, 0]\n  }'));
  assert.deepEqual(parseKind(""), {});
});

test("parseKind refuses what is not a kind file", () => {
  assert.throws(() => parseKind("["), /not valid JSON/);
  assert.throws(() => parseKind("[]"), /JSON object/);
  assert.throws(() => parseKind('{"component": "hmi/src/lib/Skid.svelte"}'), /stays inline/);
});

test("setField writes and validates each field; null deletes", () => {
  let t = apply("{}", { type: "setField", key: "type", value: "Motor" });
  t = apply(t, { type: "setField", key: "status", value: "{SpeedHz:0} Hz" });
  t = apply(t, { type: "setField", key: "labelAt", value: [0, 1.50004, 0] });
  t = apply(t, { type: "setField", key: "members", value: ["Running"] });
  t = apply(t, { type: "setField", key: "bounds", value: "auto" });
  assert.deepEqual(parseKind(t), { type: "Motor", members: ["Running"], bounds: "auto", labelAt: [0, 1.5, 0], status: "{SpeedHz:0} Hz" });
  t = apply(t, { type: "setField", key: "labelAt", value: null });
  t = apply(t, { type: "setField", key: "members", value: null });
  assert.deepEqual(parseKind(t), { type: "Motor", bounds: "auto", status: "{SpeedHz:0} Hz" });
  assert.match(refuse(t, { type: "setField", key: "model", value: "models/pump.png" }), /\.glb or \.gltf/);
  assert.match(refuse(t, { type: "setField", key: "model", value: "../pump.glb" }), /\.glb or \.gltf/);
  assert.match(refuse(t, { type: "setField", key: "labelAt", value: [0, 1] }), /labelAt/);
  assert.match(refuse(t, { type: "setField", key: "bounds", value: { size: [1, 1, 1] } }), /bounds/);
  assert.match(refuse(t, { type: "setField", key: "members", value: ["1bad"] }), /members/);
  assert.match(refuse(t, { type: "setField", key: "colour" as never, value: "red" }), /unknown field/);
});

test("drives: add at the end or at an index, update, move, delete", () => {
  let t = apply("{}", { type: "addDrive", drive: spin });
  t = apply(t, { type: "addDrive", drive: tint, index: 0 });
  assert.deepEqual(parseKind(t).drive, [tint, spin]);
  const lamp: Drive = { mesh: "Lamp", emissive: { bind: "Running", on: "running", intensity: 2 } };
  t = apply(t, { type: "updateDrive", index: 1, drive: lamp });
  assert.deepEqual(parseKind(t).drive, [tint, lamp]);
  t = apply(t, { type: "moveDrive", index: 1, to: 0 });
  assert.deepEqual(parseKind(t).drive, [lamp, tint]);
  t = apply(t, { type: "deleteDrive", index: 0 });
  assert.deepEqual(parseKind(t).drive, [tint]);
  t = apply(t, { type: "deleteDrive", index: 0 });
  assert.equal(t, "{}\n");
  assert.match(refuse(t, { type: "deleteDrive", index: 0 }), /no drive 0/);
  assert.match(refuse(t, { type: "updateDrive", index: 0, drive: tint }), /no drive 0/);
  assert.match(refuse(t, { type: "addDrive", drive: tint, index: 3 }), /out of range/);
});

test("driveError: one channel, a real axis, a member bind, a colour", () => {
  assert.equal(driveError(spin), undefined);
  assert.equal(driveError({ mesh: "F", visible: { bind: "!Tripped" } }), undefined);
  assert.equal(driveError({ mesh: "F", scale: { axis: "xyz", to: { bind: "Level", min: 0.01 } } }), undefined);
  assert.match(driveError({ mesh: "", spin: {} })!, /names a mesh/);
  assert.match(driveError({ mesh: "M" })!, /exactly one/);
  assert.match(driveError({ mesh: "M", spin: { axis: "y", revPerS: { bind: "S" } }, tint: { bind: "R", on: "x" } })!, /exactly one/);
  assert.match(driveError({ mesh: "M", spin: { axis: "xyz", revPerS: { bind: "S" } } })!, /x, y or z/);
  assert.match(driveError({ mesh: "M", spin: { axis: "y", revPerS: { bind: "1S" } } })!, /revPerS\.bind/);
  assert.match(driveError({ mesh: "M", spin: { axis: "y", revPerS: { bind: "S", scale: "2" } } })!, /scale must be a number/);
  assert.match(driveError({ mesh: "M", tint: { bind: "R" } })!, /on must be/);
  assert.match(driveError({ mesh: "M", tint: { bind: "!!R", on: "x" } })!, /bind must name/);
  assert.match(driveError({ mesh: "M", emissive: { bind: "R", on: "x", intensity: "hi" } })!, /intensity/);
  assert.match(refuse("{}", { type: "addDrive", drive: { mesh: "M" } as Drive }), /exactly one/);
});

test("a batch is all or nothing, and an op on bad JSON is refused", () => {
  const t = apply("{}", { type: "batch", ops: [{ type: "setField", key: "type", value: "Motor" }, { type: "addDrive", drive: tint }] });
  assert.deepEqual(parseKind(t), { type: "Motor", drive: [tint] });
  assert.match(refuse("{", { type: "setField", key: "type", value: "x" }), /not valid JSON/);
  assert.match(refuse("{}", { type: "batch", ops: [{ type: "setField", key: "type", value: "x" }, { type: "deleteDrive", index: 9 }] }), /no drive 9/);
});

test("ports: set, rename (keeping its place), delete; one per line between status and drive", () => {
  const withPort = apply(BASE, { type: "setPort", name: "discharge", port: { at: [0, 0.4321, -0.5], dir: [0, 1, 0] } });
  assert.match(withPort, /"status": "[^"]+",\n  "ports": \{\n    "discharge": \{ "at": \[0, 0.432, -0.5\], "dir": \[0, 1, 0\] \}\n  \},\n  "drive": \[/);
  const two = apply(withPort, { type: "setPort", name: "suction", port: { at: [0, 0.1, 0.2] } });
  assert.match(two, /"discharge": \{[^\n]*\},\n    "suction": \{ "at": \[0, 0.1, 0.2\] \}\n/);
  const renamed = apply(two, { type: "renamePort", name: "discharge", to: "outlet" });
  assert.deepEqual(Object.keys(parseKind(renamed).ports!), ["outlet", "suction"], "renaming keeps the port's place");
  assert.match(refuse(renamed, { type: "renamePort", name: "outlet", to: "suction" }), /already exists/);
  assert.match(refuse(renamed, { type: "setPort", name: "a/b", port: { at: [0, 0, 0] } }), /port name/);
  assert.match(refuse(renamed, { type: "setPort", name: "x", port: { at: [0, 0, 0], dir: [0, 0, 0] } }), /non-zero/);
  const one = apply(renamed, { type: "deletePort", name: "outlet" });
  assert.deepEqual(Object.keys(parseKind(one).ports!), ["suction"]);
  const none = apply(one, { type: "deletePort", name: "suction" });
  assert.equal(parseKind(none).ports, undefined, "the last port deleted drops the key");
  assert.equal(none, BASE);
  assert.match(refuse(none, { type: "deletePort", name: "suction" }), /no port/);
});
