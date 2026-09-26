// Reducer tests: ops against real document text, asserting on the
// re-serialized JSON. Run with `npm test` (node --test over out/).

import { strict as assert } from "node:assert";
import { test } from "node:test";
import { applyMimicOp, formatMimic, parseMimic, type MimicDoc, type MimicOp } from "./mimicOps";

const apply = (text: string, op: MimicOp): string => {
  const res = applyMimicOp(text, op);
  assert.ok("text" in res, `expected success, got: ${(res as { error: string }).error}`);
  return (res as { text: string }).text;
};

const refuse = (text: string, op: MimicOp): string => {
  const res = applyMimicOp(text, op);
  assert.ok("error" in res, "expected a refusal");
  return (res as { error: string }).error;
};

const doc = (text: string): MimicDoc => parseMimic(text);

const BASE = formatMimic({
  name: "plant",
  canvas: { width: 800, height: 400 },
  equipment: [
    { id: "tank1", component: "Tank", x: 100, y: 50, bind: { level: "LevelPct" } },
    { id: "pump1", component: "Pump", x: 300, y: 200 },
  ],
  pipes: [{ id: "pipe1", points: [[0, 100], [200, 100], [200, 300]], bind: { flowing: "PumpRun" } }],
  labels: [{ text: "city feed", x: 10, y: 90 }],
});

test("empty text parses as the starter doc; first op writes a full document", () => {
  const out = apply("", { type: "addEquipment", component: "Tank", x: 40, y: 40 });
  const d = doc(out);
  assert.equal(d.canvas.width, 960);
  assert.deepEqual(d.equipment, [{ id: "tank1", component: "Tank", x: 40, y: 40 }]);
});

test("bad JSON refuses with a message", () => {
  assert.match(refuse("{nope", { type: "setName", name: "x" }), /not valid JSON/);
  assert.match(refuse("[1,2]", { type: "setName", name: "x" }), /root must be/);
  assert.match(refuse('{"a":1}', { type: "setName", name: "x" }), /canvas/);
});

test("addEquipment generates the first free id per component", () => {
  const out = apply(BASE, { type: "addEquipment", component: "Tank", x: 1, y: 2 });
  assert.ok(doc(out).equipment!.some((e) => e.id === "tank2"));
  const out2 = apply(out, { type: "addEquipment", component: "Valve", x: 1, y: 2 });
  assert.ok(doc(out2).equipment!.some((e) => e.id === "valve1"));
});

test("moveEquipment updates position; unknown id refuses", () => {
  const out = apply(BASE, { type: "moveEquipment", id: "pump1", x: 310, y: 210 });
  const p = doc(out).equipment!.find((e) => e.id === "pump1")!;
  assert.deepEqual([p.x, p.y], [310, 210]);
  assert.match(refuse(BASE, { type: "moveEquipment", id: "ghost", x: 0, y: 0 }), /no equipment/);
});

test("updateEquipment: value sets, null deletes, id renames with collision check", () => {
  let out = apply(BASE, { type: "updateEquipment", id: "tank1", patch: { label: "T-101", width: 140 } });
  let t = doc(out).equipment!.find((e) => e.id === "tank1")!;
  assert.equal(t.label, "T-101");
  assert.equal(t.width, 140);
  out = apply(out, { type: "updateEquipment", id: "tank1", patch: { width: null } });
  t = doc(out).equipment!.find((e) => e.id === "tank1")!;
  assert.equal(t.width, undefined);
  out = apply(out, { type: "updateEquipment", id: "tank1", patch: { id: "t101" } });
  assert.ok(doc(out).equipment!.some((e) => e.id === "t101"));
  assert.match(refuse(BASE, { type: "updateEquipment", id: "tank1", patch: { id: "pump1" } }), /taken/);
  assert.match(refuse(BASE, { type: "updateEquipment", id: "tank1", patch: { id: "pipe1" } }), /taken/);
});

test("updateEquipment: renaming an id carries pipes anchored to it along (same edit)", () => {
  const anchored = apply(BASE, {
    type: "addPipe",
    id: "feed",
    points: [],
    from: { equip: "pump1", port: "out" },
    to: { equip: "tank1", port: "left" },
  });
  const withLoop = apply(anchored, {
    type: "addPipe",
    id: "loop",
    points: [],
    from: { equip: "tank1", port: "out" },
    to: { equip: "tank1", port: "in" },
  });
  const out = apply(withLoop, { type: "updateEquipment", id: "tank1", patch: { id: "t101", label: "T-101" } });
  const d = doc(out);
  const feed = d.pipes!.find((p) => p.id === "feed")!;
  assert.deepEqual(feed.from, { equip: "pump1", port: "out" });
  assert.deepEqual(feed.to, { equip: "t101", port: "left" });
  const loop = d.pipes!.find((p) => p.id === "loop")!;
  assert.deepEqual(loop.from, { equip: "t101", port: "out" });
  assert.deepEqual(loop.to, { equip: "t101", port: "in" });
  // Unanchored pipes are untouched.
  assert.deepEqual(d.pipes!.find((p) => p.id === "pipe1"), doc(BASE).pipes![0]);
  // A patch that doesn't rename leaves anchors alone.
  const same = apply(withLoop, { type: "updateEquipment", id: "tank1", patch: { id: "tank1", label: "x" } });
  assert.deepEqual(doc(same).pipes!.find((p) => p.id === "feed")!.to, { equip: "tank1", port: "left" });
});

test("setEquipmentPorts sets an override, null clears it, empty array is a valid explicit override", () => {
  const out = apply(BASE, {
    type: "setEquipmentPorts",
    id: "tank1",
    ports: [{ name: "top", x: 0.5, y: 0 }, { name: "left", x: 0, y: 0.5 }],
  });
  assert.deepEqual(doc(out).equipment!.find((e) => e.id === "tank1")!.ports, [
    { name: "top", x: 0.5, y: 0 },
    { name: "left", x: 0, y: 0.5 },
  ]);

  const cleared = apply(out, { type: "setEquipmentPorts", id: "tank1", ports: null });
  assert.equal(doc(cleared).equipment!.find((e) => e.id === "tank1")!.ports, undefined);

  const empty = apply(BASE, { type: "setEquipmentPorts", id: "tank1", ports: [] });
  assert.deepEqual(doc(empty).equipment!.find((e) => e.id === "tank1")!.ports, []);

  assert.match(refuse(BASE, { type: "setEquipmentPorts", id: "ghost", ports: [{ name: "a", x: 0, y: 0 }] }), /no equipment/);
  assert.match(
    refuse(BASE, {
      type: "setEquipmentPorts",
      id: "tank1",
      ports: [{ name: "a", x: 0, y: "x" }] as unknown as { name: string; x: number; y: number }[],
    }),
    /ports/
  );
  assert.match(
    refuse(BASE, {
      type: "setEquipmentPorts",
      id: "tank1",
      ports: [
        { name: "dup", x: 0, y: 0 },
        { name: "dup", x: 1, y: 1 },
      ],
    }),
    /ports/
  );
});

test("setEquipmentPorts: dir is optional, validated against the 4-value enum", () => {
  const out = apply(BASE, {
    type: "setEquipmentPorts",
    id: "tank1",
    ports: [{ name: "top", x: 0.5, y: 0, dir: "up" }],
  });
  assert.deepEqual(doc(out).equipment!.find((e) => e.id === "tank1")!.ports, [
    { name: "top", x: 0.5, y: 0, dir: "up" },
  ]);
  assert.match(
    refuse(BASE, {
      type: "setEquipmentPorts",
      id: "tank1",
      ports: [{ name: "top", x: 0.5, y: 0, dir: "sideways" } as unknown as { name: string; x: number; y: number }],
    }),
    /ports/
  );
});

test("delete ops remove exactly the target", () => {
  const out = apply(BASE, { type: "deleteEquipment", id: "pump1" });
  assert.deepEqual(doc(out).equipment!.map((e) => e.id), ["tank1"]);
  const out2 = apply(BASE, { type: "deletePipe", id: "pipe1" });
  assert.equal(doc(out2).pipes, undefined); // empty collections are omitted
  const out3 = apply(BASE, { type: "deleteLabel", index: 0 });
  assert.equal(doc(out3).labels, undefined);
  assert.match(refuse(BASE, { type: "deleteLabel", index: 5 }), /no label/);
});

test("pipes: add generates pipeN, setPipePoints validates shape", () => {
  const out = apply(BASE, { type: "addPipe", points: [[0, 0], [50, 0]] });
  assert.ok(doc(out).pipes!.some((p) => p.id === "pipe2"));
  const out2 = apply(BASE, { type: "setPipePoints", id: "pipe1", points: [[1, 2], [3, 4]] });
  assert.deepEqual(doc(out2).pipes![0].points, [[1, 2], [3, 4]]);
  assert.match(refuse(BASE, { type: "addPipe", points: [[1, 2]] }), /two/);
  assert.match(
    refuse(BASE, { type: "setPipePoints", id: "pipe1", points: [[1], [2]] as unknown as [number, number][] }),
    /two/
  );
});

// Mirrors the mimic editor's node-multiselect delete gesture
// (EditorCanvas.svelte's onkeydown): filter the selected indices out of the
// pipe's points, then setPipePoints if >=2 remain or deletePipe otherwise.
// This is the reducer half of that gesture's contract — the webview's own
// tests (mimicOps.test.ts covers the host; EditorCanvas has no unit test
// harness) cover the op-selection side.
test("multi-node delete: setPipePoints with the survivors, or deletePipe below 2", () => {
  const FOUR = formatMimic({
    canvas: { width: 800, height: 400 },
    pipes: [{ id: "discharge", points: [[262, 385], [380, 385], [380, 120], [435, 120]] }],
  });

  // Delete a single (multi-select of size 1) middle node: 4 -> 3 points.
  const indices1 = [1];
  const pts1 = doc(FOUR).pipes![0].points.filter((_, i) => !indices1.includes(i));
  const out1 = apply(FOUR, { type: "setPipePoints", id: "discharge", points: pts1 });
  assert.deepEqual(doc(out1).pipes![0].points, [[262, 385], [380, 120], [435, 120]]);

  // Delete two middle nodes: 4 -> 2 points (still a valid pipe).
  const indices2 = [1, 2];
  const pts2 = doc(FOUR).pipes![0].points.filter((_, i) => !indices2.includes(i));
  const out2 = apply(FOUR, { type: "setPipePoints", id: "discharge", points: pts2 });
  assert.deepEqual(doc(out2).pipes![0].points, [[262, 385], [435, 120]]);

  // Delete three of four nodes: 1 point would remain -> the gesture falls
  // back to deletePipe instead of posting an invalid setPipePoints.
  const indices3 = [0, 1, 2];
  const pts3 = doc(FOUR).pipes![0].points.filter((_, i) => !indices3.includes(i));
  assert.equal(pts3.length, 1);
  const out3 = apply(FOUR, { type: "deletePipe", id: "discharge" });
  assert.equal(doc(out3).pipes, undefined);

  // The reducer itself still refuses a setPipePoints down to <2 (defense in
  // depth if a caller ever violates the fallback convention above).
  assert.match(refuse(FOUR, { type: "setPipePoints", id: "discharge", points: [[262, 385]] }), /two/);
});

test("pipes: from/to anchors — addPipe accepts fewer interior points per anchored end, validates anchor shape", () => {
  // Both ends anchored: 0 interior points is valid.
  const out = apply(BASE, {
    type: "addPipe",
    points: [],
    from: { equip: "pump1", port: "out" },
    to: { equip: "tank1", port: "left" },
  });
  const p = doc(out).pipes!.find((p) => p.points.length === 0)!;
  assert.deepEqual(p.from, { equip: "pump1", port: "out" });
  assert.deepEqual(p.to, { equip: "tank1", port: "left" });

  // One end anchored: 1 interior point is valid, 0 is not.
  const out2 = apply(BASE, { type: "addPipe", points: [[1, 2]], from: { equip: "pump1", port: "out" } });
  assert.deepEqual(doc(out2).pipes!.find((p) => p.from)!.points, [[1, 2]]);
  assert.match(refuse(BASE, { type: "addPipe", points: [], from: { equip: "pump1", port: "out" } }), /at least/);

  // No anchors: the historical 2-point floor still applies.
  assert.match(refuse(BASE, { type: "addPipe", points: [[1, 2]] }), /two/);

  // Malformed anchor shape refuses.
  assert.match(
    refuse(BASE, { type: "addPipe", points: [], from: { equip: "pump1" } as unknown as { equip: string; port: string } }),
    /from/
  );
});

test("pipes: the port-to-port addPipe the + Pipe gesture posts is structured-clone-safe and applies as received", () => {
  // The webview builds this op from Svelte $state anchors (Proxies), which
  // postMessage's structured clone refuses; it copies the op to plain data
  // first (webview-ui/src/mimic/opPayload.ts). Stand in for that here: a
  // Proxy-built op must NOT clone, its JSON copy must, and what arrives
  // applies — both ends anchored, suggested orthogonal corners.
  const from = new Proxy({ equip: "pump1", port: "out" }, {});
  const to = new Proxy({ equip: "tank1", port: "left" }, {});
  const raw = { type: "addPipe" as const, points: [[358, 150], [60, 150]] as [number, number][], from, to, routing: "orthogonal" as const };
  assert.throws(() => structuredClone(raw), (e: Error) => e.name === "DataCloneError");
  const received = structuredClone(JSON.parse(JSON.stringify(raw))) as MimicOp;
  const p = doc(apply(BASE, received)).pipes!.find((q) => q.from)!;
  assert.deepEqual(p.from, { equip: "pump1", port: "out" });
  assert.deepEqual(p.to, { equip: "tank1", port: "left" });
  assert.equal(p.routing, "orthogonal");
  assert.deepEqual(p.points, [[358, 150], [60, 150]]);
});

test("pipes: addPipe accepts an optional routing (the route-suggestion gesture's port-to-port default)", () => {
  const out = apply(BASE, {
    type: "addPipe",
    points: [[150, 100]],
    from: { equip: "pump1", port: "out" },
    to: { equip: "tank1", port: "left" },
    routing: "orthogonal",
  });
  const p = doc(out).pipes!.find((p) => p.from)!;
  assert.equal(p.routing, "orthogonal");

  // Absent routing (every pre-existing addPipe call site) is still 'direct'
  // by omission — no routing key written at all.
  const out2 = apply(BASE, { type: "addPipe", points: [[1, 2], [3, 4]] });
  assert.equal(doc(out2).pipes!.find((p) => p.points.length === 2)!.routing, undefined);

  assert.match(
    refuse(BASE, { type: "addPipe", points: [[1, 2], [3, 4]], routing: "diagonal" as "direct" }),
    /routing/
  );
});

test("pipes: setPipePoints respects the pipe's CURRENT anchor state (not the caller's)", () => {
  const anchored = apply(BASE, {
    type: "addPipe",
    id: "anchored",
    points: [],
    from: { equip: "pump1", port: "out" },
    to: { equip: "tank1", port: "left" },
  });
  // Both ends anchored -> empty points is fine.
  const out = apply(anchored, { type: "setPipePoints", id: "anchored", points: [] });
  assert.deepEqual(doc(out).pipes!.find((p) => p.id === "anchored")!.points, []);
  // A plain (unanchored) pipe still needs 2.
  assert.match(refuse(BASE, { type: "setPipePoints", id: "pipe1", points: [] }), /two/);
});

test("pipes: updatePipe sets/clears from/to, validates shape, and keeps points consistent with the resulting anchor state", () => {
  // Set an anchor via patch.
  const out = apply(BASE, { type: "updatePipe", id: "pipe1", patch: { from: { equip: "pump1", port: "out" } } });
  assert.deepEqual(doc(out).pipes![0].from, { equip: "pump1", port: "out" });

  // Clear it again (null).
  const out2 = apply(out, { type: "updatePipe", id: "pipe1", patch: { from: null } });
  assert.equal(doc(out2).pipes![0].from, undefined);

  // Malformed anchor shape refuses.
  assert.match(
    refuse(BASE, { type: "updatePipe", id: "pipe1", patch: { to: { port: "out" } } }),
    /to/
  );

  // Anchoring BOTH ends of a pipe that already has 3 stored points (now all
  // "interior") is fine — the floor only ever shrinks, never grows what's
  // already there.
  const bothAnchored = apply(BASE, {
    type: "updatePipe",
    id: "pipe1",
    patch: { from: { equip: "pump1", port: "out" }, to: { equip: "tank1", port: "left" } },
  });
  assert.deepEqual(doc(bothAnchored).pipes![0].points.length, 3);

  // Anchoring both ends of a pipe with fewer than the shrunk floor allows
  // (0 here) via patch.points in the SAME op is fine...
  const shrunk = apply(BASE, {
    type: "updatePipe",
    id: "pipe1",
    patch: { from: { equip: "pump1", port: "out" }, to: { equip: "tank1", port: "left" }, points: [] },
  });
  assert.deepEqual(doc(shrunk).pipes![0].points, []);

  // ...but CLEARING an anchor on a pipe already down to 0 interior points,
  // without also patching points back up, refuses rather than leaving an
  // invalid (<2, unanchored) pipe.
  assert.match(
    refuse(shrunk, { type: "updatePipe", id: "pipe1", patch: { to: null } }),
    /at least/
  );
});

test("pipes: routing sets/clears through updatePipe, validates the enum", () => {
  const out = apply(BASE, { type: "updatePipe", id: "pipe1", patch: { routing: "orthogonal" } });
  assert.equal(doc(out).pipes![0].routing, "orthogonal");
  // null clears back to the default (absent = "direct").
  const out2 = apply(out, { type: "updatePipe", id: "pipe1", patch: { routing: null } });
  assert.equal(doc(out2).pipes![0].routing, undefined);
  assert.match(
    refuse(BASE, { type: "updatePipe", id: "pipe1", patch: { routing: "diagonal" } }),
    /direct.*orthogonal/
  );
});

test("labels add and update", () => {
  const out = apply(BASE, { type: "addLabel", text: "zone 13", x: 700, y: 90 });
  assert.equal(doc(out).labels!.length, 2);
  const out2 = apply(out, { type: "updateLabel", index: 1, patch: { text: "zone 14", x: 710 } });
  assert.deepEqual(doc(out2).labels![1], { text: "zone 14", x: 710, y: 90 });
});

test("setCanvas and setName", () => {
  const out = apply(BASE, { type: "setCanvas", width: 1024, height: 512 });
  assert.deepEqual(doc(out).canvas, { width: 1024, height: 512 });
  assert.match(refuse(BASE, { type: "setCanvas", width: 0, height: 100 }), /positive/);
  const out2 = apply(BASE, { type: "setName", name: null });
  assert.equal(doc(out2).name, undefined);
});

test("canonical format: point pairs inline, stable key order, trailing newline", () => {
  const text = formatMimic(parseMimic(BASE));
  assert.ok(text.endsWith("\n"));
  assert.match(text, /\[0, 100\]/, "point pairs collapse onto one line");
  // Round-trip is a fixed point: format(parse(format(x))) === format(x).
  assert.equal(formatMimic(parseMimic(text)), text);
  // Key order: id before component before x.
  const eqBlock = text.slice(text.indexOf('"equipment"'));
  assert.ok(eqBlock.indexOf('"id"') < eqBlock.indexOf('"component"'));
});

test("batch applies atomically: all ops or none", () => {
  const out = apply(BASE, {
    type: "batch",
    ops: [
      { type: "moveEquipment", id: "pump1", x: 320, y: 220 },
      { type: "setPipePoints", id: "pipe1", points: [[0, 120], [200, 120], [200, 320]] },
    ],
  });
  const d = doc(out);
  assert.deepEqual(d.equipment!.find((e) => e.id === "pump1")!.x, 320);
  assert.deepEqual(d.pipes![0].points[2], [200, 320]);
  // Second op refuses -> nothing applies (refusals never write).
  assert.match(
    refuse(BASE, {
      type: "batch",
      ops: [
        { type: "moveEquipment", id: "pump1", x: 1, y: 1 },
        { type: "setPipePoints", id: "ghost", points: [[0, 0], [1, 1]] },
      ],
    }),
    /no pipe/
  );
});

test("unknown fields in a hand-written doc survive a round-trip", () => {
  const hand = JSON.stringify({
    canvas: { width: 100, height: 100 },
    equipment: [{ id: "e1", component: "Tank", x: 0, y: 0, custom: "kept" }],
    extra: { note: "kept too" },
  });
  const out = apply(hand, { type: "moveEquipment", id: "e1", x: 5, y: 5 });
  const d = doc(out) as MimicDoc & { extra?: unknown };
  assert.deepEqual(d.extra, { note: "kept too" });
  assert.equal((d.equipment![0] as unknown as { custom: string }).custom, "kept");
});
