// Reducer tests: ops against real document text, asserting on the
// re-serialized JSON. Run with `npm test` (node --test over out/).

import { strict as assert } from "node:assert";
import { test } from "node:test";
import { applySceneOp, formatScene, parseScene, type SceneDoc, type SceneOp } from "./sceneOps";

const apply = (text: string, op: SceneOp): string => {
  const res = applySceneOp(text, op);
  assert.ok("text" in res, `expected success, got: ${(res as { error: string }).error}`);
  return (res as { text: string }).text;
};

const refuse = (text: string, op: SceneOp): string => {
  const res = applySceneOp(text, op);
  assert.ok("error" in res, "expected a refusal");
  return (res as { error: string }).error;
};

const doc = (text: string): SceneDoc => parseScene(text);

const BASE = formatScene({
  name: "rig",
  kinds: { pump: { model: "models/pump.glb" } },
  camera: { pos: [2, 1.5, 2.5], target: [0, 0, 0] },
  grid: { size: [4, 4], cell: 0.1 },
  fixtures: [{ kind: "marker", pos: [0, 0.001, 0], size: [0.08, 0.08] }],
  nodes: [
    { id: "T101", kind: "tank", tag: "T101", label: "T-101", pos: [1, -0.75, 0.4] },
    { id: "P101", kind: "pump", tag: "P101", pos: [0.3, 0, 0.2], rot: [0, 90, 0] },
  ],
  pipes: [{ points: [[0.1, 0.16, 0], [0.1, 0.2, 0], [0.42, 0.2, 0]], bind: { flowing: "P101.Running" } }],
});

test("empty text parses as the starter doc; the first op writes a full document", () => {
  const out = apply("", { type: "addNode", kind: "pump", pos: [0.5, 0, 0.5] });
  assert.deepEqual(doc(out), { nodes: [{ id: "PUMP1", kind: "pump", pos: [0.5, 0, 0.5] }] });
});

test("bad JSON refuses with a message", () => {
  assert.match(refuse("{ nodes: [", { type: "deleteNode", id: "x" }), /not valid JSON/);
  assert.match(refuse("[1]", { type: "deleteNode", id: "x" }), /root must be/);
});

test("canonical format: key order, triples on one line, empties dropped", () => {
  assert.ok(BASE.startsWith('{\n  "name": "rig",\n  "kinds": {'), BASE.slice(0, 40));
  assert.ok(BASE.includes('"pos": [1, -0.75, 0.4]'), "a triple stays on one line");
  assert.ok(BASE.includes('"size": [0.08, 0.08]'), "a pair stays on one line");
  assert.ok(BASE.includes('"size": [4, 4]'), "the grid pair too");
  assert.ok(BASE.indexOf('"camera"') < BASE.indexOf('"grid"') && BASE.indexOf('"grid"') < BASE.indexOf('"nodes"'));
  const noPipes = formatScene({ nodes: [], pipes: [], fixtures: [], kinds: {} });
  assert.equal(noPipes, '{\n  "nodes": []\n}\n');
  // Unknown keys pass through (a field this reducer does not know yet).
  const extra = formatScene({ nodes: [], environment: { hdri: "env/a.hdr" }, look: "paper" } as SceneDoc);
  assert.ok(extra.includes('"look": "paper"') && extra.includes('"hdri"'));
});

test("addNode generates an upper-case id from the kind, skipping taken ones", () => {
  const out = apply(BASE, { type: "addNode", kind: "pump", pos: [0.123456, 0, -1.9999] });
  const d = doc(out);
  assert.equal(d.nodes.length, 3);
  assert.deepEqual(d.nodes[2], { id: "PUMP1", kind: "pump", pos: [0.123, 0, -2] });
  const again = apply(out, { type: "addNode", kind: "pump", pos: [0, 0, 0], tag: "P102", label: "P-102" });
  assert.deepEqual(doc(again).nodes[3], { id: "PUMP2", kind: "pump", tag: "P102", label: "P-102", pos: [0, 0, 0] });
  assert.match(refuse(BASE, { type: "addNode", kind: "pump", pos: [0, 0, 0], id: "T101" }), /already taken/);
  assert.match(refuse(BASE, { type: "addNode", kind: "pump", pos: [0, 0, 0], id: "bad id" }), /must be a name/);
  assert.match(refuse(BASE, { type: "addNode", kind: "pump", pos: [0, 0] as never }), /pos must be/);
});

test("moveNode rounds to millimetres and tenth-degrees; a zero rotation is dropped", () => {
  const out = apply(BASE, { type: "moveNode", id: "P101", pos: [0.30049, 0.0004, 0.2], rot: [0, 44.96, 0] });
  const p = doc(out).nodes[1];
  assert.deepEqual(p.pos, [0.3, 0, 0.2]);
  assert.deepEqual(p.rot, [0, 45, 0]);
  const flat = apply(out, { type: "moveNode", id: "P101", pos: [0.3, 0, 0.2], rot: [0, 0.04, 0] });
  assert.equal(doc(flat).nodes[1].rot, undefined, "a rotation that rounds to zero is not written");
  const keep = apply(BASE, { type: "moveNode", id: "P101", pos: [1, 1, 1] });
  assert.deepEqual(doc(keep).nodes[1].rot, [0, 90, 0], "no rot in the op leaves the rotation alone");
  assert.match(refuse(BASE, { type: "moveNode", id: "nope", pos: [0, 0, 0] }), /no node "nope"/);
});

test("a move diffs as one line", () => {
  const out = apply(BASE, { type: "moveNode", id: "T101", pos: [1.2, -0.75, 0.4] });
  const before = BASE.split("\n");
  const after = out.split("\n");
  assert.equal(before.length, after.length);
  const changed = before.filter((l, i) => l !== after[i]);
  assert.deepEqual(changed, ['      "pos": [1, -0.75, 0.4]']);
});

test("updateNode patches fields, null deletes, a rename must be free", () => {
  const out = apply(BASE, { type: "updateNode", id: "P101", patch: { label: "P-101", scale: 1.5, tag: null, bind: { speed: "VFD1.Hz" } } });
  const p = doc(out).nodes[1];
  assert.equal(p.label, "P-101");
  assert.equal(p.scale, 1.5);
  assert.equal(p.tag, undefined);
  assert.deepEqual(p.bind, { speed: "VFD1.Hz" });
  const renamed = apply(out, { type: "updateNode", id: "P101", patch: { id: "P102" } });
  assert.deepEqual(doc(renamed).nodes.map((n) => n.id), ["T101", "P102"]);
  assert.match(refuse(BASE, { type: "updateNode", id: "P101", patch: { id: "T101" } }), /already taken/);
  assert.match(refuse(BASE, { type: "updateNode", id: "P101", patch: { scale: -1 } }), /scale must be/);
  assert.match(refuse(BASE, { type: "updateNode", id: "P101", patch: { bind: { a: 1 } } }), /bind must map/);
  // An empty label or tag clears the field rather than writing "".
  const cleared = apply(BASE, { type: "updateNode", id: "T101", patch: { label: "" } });
  assert.equal(doc(cleared).nodes[0].label, undefined);
  const unit = apply(out, { type: "updateNode", id: "P101", patch: { scale: 1 } });
  assert.equal(doc(unit).nodes[1].scale, undefined, "scale 1 is the default and is not written");
});

test("deleteNode removes the node and nothing else", () => {
  const out = apply(BASE, { type: "deleteNode", id: "T101" });
  const d = doc(out);
  assert.deepEqual(d.nodes.map((n) => n.id), ["P101"]);
  assert.equal(d.pipes?.length, 1);
  assert.match(refuse(out, { type: "deleteNode", id: "T101" }), /no node/);
});

test("pipe ops work by index, since a pipe may have no id", () => {
  const out = apply(BASE, { type: "setPipePoints", index: 0, points: [[0, 0, 0], [1, 0.00049, 0]] });
  assert.deepEqual(doc(out).pipes?.[0].points, [[0, 0, 0], [1, 0, 0]]);
  assert.deepEqual(doc(out).pipes?.[0].bind, { flowing: "P101.Running" }, "the bind survives");
  assert.match(refuse(BASE, { type: "setPipePoints", index: 0, points: [[0, 0, 0]] }), /at least two/);
  const added = apply(out, { type: "addPipe", points: [[0, 0, 0], [0, 1, 0]], bind: { flowing: "X" } });
  assert.equal(doc(added).pipes?.length, 2);
  const gone = apply(added, { type: "deletePipe", index: 0 });
  assert.deepEqual(doc(gone).pipes?.[0].bind, { flowing: "X" });
  assert.match(refuse(gone, { type: "deletePipe", index: 5 }), /no pipe #5/);
});

test("setCamera writes the view and keeps the fov", () => {
  const withFov = apply(BASE, { type: "updateNode", id: "T101", patch: {} });
  const d = doc(withFov);
  d.camera!.fov = 35;
  const out = apply(formatScene(d), { type: "setCamera", pos: [3.00001, 2, 1], target: [0.5, 0, 0] });
  assert.deepEqual(doc(out).camera, { pos: [3, 2, 1], target: [0.5, 0, 0], fov: 35 });
});

test("batch is atomic: a refused sub-op leaves the text alone", () => {
  const ok = apply(BASE, { type: "batch", ops: [{ type: "moveNode", id: "T101", pos: [0, 0, 0] }, { type: "deleteNode", id: "P101" }] });
  assert.deepEqual(doc(ok).nodes.map((n) => n.id), ["T101"]);
  assert.match(refuse(BASE, { type: "batch", ops: [{ type: "moveNode", id: "T101", pos: [0, 0, 0] }, { type: "deleteNode", id: "zz" }] }), /no node "zz"/);
});

test("a hand-written document normalizes on its first edit, kinds and environment untouched", () => {
  const hand = `{"nodes":[{"pos":[0,0,0],"id":"A","kind":"tank"}],"kinds":{"tank":{"model":"m.glb","drive":[{"mesh":"Fluid","scale":{"axis":"y","to":{"bind":"Level"}}}]}},"environment":{"hdri":"e.hdr"}}`;
  const out = apply(hand, { type: "moveNode", id: "A", pos: [1, 0, 0] });
  const d = doc(out);
  assert.deepEqual(d.kinds, JSON.parse(hand).kinds);
  assert.deepEqual(d.environment, { hdri: "e.hdr" });
  assert.ok(out.startsWith('{\n  "kinds": {'));
  assert.ok(out.includes('"id": "A",\n      "kind": "tank",\n      "pos": [1, 0, 0]'));
});
