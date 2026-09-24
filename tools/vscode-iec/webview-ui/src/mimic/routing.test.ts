/// <reference types="node" />
// Pure-math coverage for orthogonal pipe routing (hmi/src/lib/mimic.ts's
// orthogonalPoints/routedPoints — the SAME function EditorCanvas.svelte and
// the runtime <Mimic> both call, so editor and runtime always draw
// identically). Like portsGestures.test.ts, runs directly with
// `node --experimental-strip-types --test` — no svelte/vscode imports.
// Requires the hmi package's dist to be current: `npm run package` in hmi/
// (or `npm run build` there) after editing hmi/src/lib/mimic.ts. Imports the
// "./mimic" subpath (not the package root) so plain node --test doesn't
// also have to load the root's Svelte component re-exports.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  inferPortDir,
  makeGetPort,
  orthogonalPoints,
  resolvedPortDir,
  resolvePipeEndpoints,
  routedPoints,
  PORT_STUB,
  type EquipmentBox,
  type GetPort,
  type MimicPort,
} from "@joyautomation/nautilus-hmi/mimic";

test("orthogonalPoints: already-axis-aligned pairs pass through with no corner", () => {
  // A horizontal-then-vertical run has no diagonals to begin with.
  assert.deepEqual(
    orthogonalPoints([
      [0, 0],
      [100, 0],
      [100, 50],
    ]),
    [
      [0, 0],
      [100, 0],
      [100, 50],
    ]
  );
});

test("orthogonalPoints: a single diagonal pair gets one corner, dominant axis first", () => {
  // dx=100 > dy=50 -> horizontal leg first (continues to x=100 at the
  // original y), then vertical to the destination.
  assert.deepEqual(orthogonalPoints([[0, 0], [100, 50]]), [
    [0, 0],
    [100, 0],
    [100, 50],
  ]);
  // dy=100 > dx=50 -> vertical leg first.
  assert.deepEqual(orthogonalPoints([[0, 0], [50, 100]]), [
    [0, 0],
    [0, 100],
    [50, 100],
  ]);
});

test("orthogonalPoints: consecutive diagonal segments chain by continuing the incoming axis first", () => {
  // Segment 1 (0,0)->(100,50): dx dominant -> horizontal first, arrives
  // having just moved VERTICALLY (the second leg) -> lastAxis='v'.
  // Segment 2 (100,50)->(150,150): continues vertical first (not
  // re-deriving from THIS segment's own dominant axis, which would also be
  // vertical here since dy=100>dx=50 — so this case alone doesn't
  // distinguish the rule; see the next assertion for a case where it does).
  const pts = orthogonalPoints([
    [0, 0],
    [100, 50],
    [150, 150],
  ]);
  assert.deepEqual(pts, [
    [0, 0],
    [100, 0],
    [100, 50],
    [100, 150],
    [150, 150],
  ]);

  // Segment 1 (0,0)->(100,100): equal dx/dy -> horizontal first (>=), arrives
  // via a vertical leg -> lastAxis='v'. Segment 2 (100,100)->(150,90): this
  // segment's OWN dominant axis is horizontal (dx=50 > dy=10), but the rule
  // continues the incoming (vertical) axis first instead — proving it's not
  // just re-picking the dominant axis per segment.
  const chained = orthogonalPoints([
    [0, 0],
    [100, 100],
    [150, 90],
  ]);
  assert.deepEqual(chained, [
    [0, 0],
    [100, 0],
    [100, 100],
    [100, 90], // continues vertical first from the incoming axis...
    [150, 90], // ...then horizontal to the destination.
  ]);
});

test("orthogonalPoints: fewer than 2 points, or already-orthogonal pairs mid-route, pass through unchanged", () => {
  assert.deepEqual(orthogonalPoints([]), []);
  assert.deepEqual(orthogonalPoints([[5, 5]]), [[5, 5]]);
});

test("routedPoints: 'orthogonal' routes, absent/'direct' passes the raw points through", () => {
  const points: [number, number][] = [
    [0, 0],
    [100, 50],
  ];
  assert.deepEqual(routedPoints({ points, routing: "orthogonal" }), [
    [0, 0],
    [100, 0],
    [100, 50],
  ]);
  assert.deepEqual(routedPoints({ points, routing: "direct" }), points);
  assert.deepEqual(routedPoints({ points }), points);
});

// ── dir inference (hmi/src/lib/mimic.ts's inferPortDir/resolvedPortDir) ────

test("inferPortDir: an edge midpoint infers the direction that edge faces", () => {
  assert.equal(inferPortDir({ x: 0, y: 0.5 }), "left");
  assert.equal(inferPortDir({ x: 1, y: 0.5 }), "right");
  assert.equal(inferPortDir({ x: 0.5, y: 0 }), "up");
  assert.equal(inferPortDir({ x: 0.5, y: 1 }), "down");
  // Any non-corner point on the edge, not just the midpoint.
  assert.equal(inferPortDir({ x: 0, y: 0.2 }), "left");
});

test("inferPortDir: a corner (both axes on an edge) has no inferred direction", () => {
  assert.equal(inferPortDir({ x: 0, y: 0 }), undefined);
  assert.equal(inferPortDir({ x: 1, y: 0 }), undefined);
  assert.equal(inferPortDir({ x: 1, y: 1 }), undefined);
  assert.equal(inferPortDir({ x: 0, y: 1 }), undefined);
});

test("inferPortDir: an interior point (neither axis on an edge) has no inferred direction", () => {
  assert.equal(inferPortDir({ x: 0.5, y: 0.5 }), undefined);
  assert.equal(inferPortDir({ x: 0.3, y: 0.7 }), undefined);
});

test("resolvedPortDir: explicit dir wins over inference, including on a corner/interior port", () => {
  assert.equal(resolvedPortDir({ x: 0, y: 0.5, dir: "up" }), "up");
  assert.equal(resolvedPortDir({ x: 0, y: 0 }), undefined);
  assert.equal(resolvedPortDir({ x: 0, y: 0, dir: "down" }), "down");
  // Absent dir falls through to inference.
  assert.equal(resolvedPortDir({ x: 1, y: 0.5 }), "right");
});

// ── pipe anchors (resolvePipeEndpoints) ─────────────────────────────────────

const box = (x: number, y: number, w: number, h: number): EquipmentBox => ({ x, y, w, h });

/** A tiny two-equipment fixture: "pump" (100x60 @ 0,0, port "out" @ x=1,y=0.5
 * -> right, absolute (100, 30)) and "tank" (100x100 @ 300,-20, port "left" @
 * x=0,y=0.5 -> left, absolute (300, 30) — same y as the pump's port, so the
 * two-anchor test below is a straight horizontal run once stubbed). */
function fixtureGetPort(): GetPort {
  const boxes: Record<string, EquipmentBox> = { pump: box(0, 0, 100, 60), tank: box(300, -20, 100, 100) };
  const ports: Record<string, MimicPort[]> = {
    pump: [{ name: "out", x: 1, y: 0.5 }],
    tank: [{ name: "left", x: 0, y: 0.5 }, { name: "corner", x: 1, y: 1 }],
  };
  return makeGetPort(
    (equip) => boxes[equip],
    (equip) => ports[equip]
  );
}

test("resolvePipeEndpoints: both ends anchored resolves absolute positions, dirs, no flags", () => {
  const getPort = fixtureGetPort();
  const r = resolvePipeEndpoints(
    { points: [], from: { equip: "pump", port: "out" }, to: { equip: "tank", port: "left" } },
    getPort
  );
  assert.deepEqual(r.points, [[100, 30], [300, 30]]);
  assert.equal(r.startDir, "right");
  assert.equal(r.endDir, "left");
  assert.equal(r.startFlagged, false);
  assert.equal(r.endFlagged, false);
});

test("resolvePipeEndpoints: one end anchored, interior points preserved between anchor and the other (plain) end", () => {
  const getPort = fixtureGetPort();
  const r = resolvePipeEndpoints(
    { points: [[150, 30], [150, 200]], from: { equip: "pump", port: "out" } },
    getPort
  );
  assert.deepEqual(r.points, [[100, 30], [150, 30], [150, 200]]);
  assert.equal(r.startDir, "right");
  assert.equal(r.endDir, undefined);
});

test("resolvePipeEndpoints: an anchored port with no inferred direction (a corner) has no dir unless the port sets one explicitly", () => {
  const getPort = fixtureGetPort();
  const r = resolvePipeEndpoints({ points: [[200, 200]], to: { equip: "tank", port: "corner" } }, getPort);
  assert.equal(r.endDir, undefined);
});

test("resolvePipeEndpoints: an unresolved anchor (unknown equip/port) flags that end and falls back to the nearest interior vertex", () => {
  const getPort = fixtureGetPort();
  const r = resolvePipeEndpoints(
    { points: [[50, 50], [250, 50]], from: { equip: "ghost", port: "nope" } },
    getPort
  );
  assert.equal(r.startFlagged, true);
  // Falls back to the first interior point rather than throwing or vanishing.
  assert.deepEqual(r.points, [[50, 50], [50, 50], [250, 50]]);
});

test("resolvePipeEndpoints: both ends unresolved with no interior points falls back to the origin rather than crashing", () => {
  const getPort: GetPort = () => undefined;
  const r = resolvePipeEndpoints(
    { points: [], from: { equip: "ghost", port: "a" }, to: { equip: "ghost", port: "b" } },
    getPort
  );
  assert.equal(r.startFlagged, true);
  assert.equal(r.endFlagged, true);
  assert.deepEqual(r.points, [[0, 0], [0, 0]]);
});

test("resolvePipeEndpoints: an unanchored plain pipe is unaffected (no getPort call needed)", () => {
  const r = resolvePipeEndpoints({ points: [[0, 0], [10, 10]] }, () => {
    throw new Error("should not be called");
  });
  assert.deepEqual(r.points, [[0, 0], [10, 10]]);
  assert.equal(r.startFlagged, false);
  assert.equal(r.endFlagged, false);
});

// ── exit-direction stubs (routedPoints honoring dir at an anchored end) ────

test("routedPoints: a 'direct' pipe anchored to a directional port gets a stub then a straight line — no corner logic", () => {
  const getPort = fixtureGetPort();
  const pts = routedPoints(
    { points: [[100, 300]], from: { equip: "pump", port: "out" }, routing: "direct" },
    getPort
  );
  // pump "out" is at (100, 30), dir "right" -> stub PORT_STUB px further right,
  // then straight to the (unrouted) interior point.
  assert.deepEqual(pts, [
    [100, 30],
    [100 + PORT_STUB, 30],
    [100, 300],
  ]);
});

test("routedPoints: an 'orthogonal' pipe anchored to a directional port corners only AFTER the stub", () => {
  const getPort = fixtureGetPort();
  // pump "out" (100, 30, dir right) -> some point straight below-right of it.
  const pts = routedPoints(
    { points: [], from: { equip: "pump", port: "out" }, to: { equip: "tank", port: "corner" }, routing: "orthogonal" },
    getPort
  );
  // tank "corner" (400, 80) has no dir (a corner port) -> no stub on that end.
  // Expect: stub leaves (100,30) horizontally, THEN the orthogonal corner
  // logic takes over from the stub point onward (dominant-axis rule).
  assert.deepEqual(pts, [
    [100, 30],
    [100 + PORT_STUB, 30],
    [400, 30],
    [400, 80],
  ]);
});

test("routedPoints: anchored both ends with opposing dirs emits a stub at each end", () => {
  const getPort = fixtureGetPort();
  const pts = routedPoints(
    { points: [], from: { equip: "pump", port: "out" }, to: { equip: "tank", port: "left" }, routing: "orthogonal" },
    getPort
  );
  // pump "out" (100,30, right) and tank "left" (300,30, left) — both stubs
  // point toward each other along the SAME y (mirroring the P-101 -> E-101
  // style straight horizontal run the demo doc uses), so orthogonalPoints
  // sees every consecutive pair already axis-aligned — no further corner.
  assert.deepEqual(pts, [
    [100, 30],
    [100 + PORT_STUB, 30],
    [300 - PORT_STUB, 30],
    [300, 30],
  ]);
});

test("routedPoints: a plain (unanchored) pipe never calls getPort and is unaffected by this feature", () => {
  const pts = routedPoints({ points: [[0, 0], [10, 0]], routing: "orthogonal" }, () => {
    throw new Error("should not be called");
  });
  assert.deepEqual(pts, [[0, 0], [10, 0]]);
});
