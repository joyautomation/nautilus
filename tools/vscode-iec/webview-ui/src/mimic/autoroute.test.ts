/// <reference types="node" />
// Coverage for the route-suggestion generator (autoroute.ts): shape
// selection order, obstacle avoidance, determinism, and PORT_STUB
// compliance at directional ends. Like routing.test.ts, runs directly with
// `node --experimental-strip-types --test` — no svelte/vscode imports.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { PORT_STUB } from "@joyautomation/nautilus-hmi/mimic";
import { faceDir, suggestRoute, type ObstacleRect } from "./autoroute.ts";

test("no obstacles, already aligned: straight route (zero interior corners)", () => {
  const pts = suggestRoute({ x: 0, y: 50 }, undefined, { x: 200, y: 50 }, undefined, []);
  assert.deepEqual(pts, []);
});

test("no obstacles, offset diagonally: single-corner L, dominant axis first", () => {
  // dx=200 > dy=50 -> horizontal leg first, i.e. the corner sits at
  // (end.x, start.y) — the SAME "dominant axis first" convention as
  // hmi/src/lib/mimic.ts's orthogonalPoints and the freehand draw snap.
  const pts = suggestRoute({ x: 0, y: 0 }, undefined, { x: 200, y: 50 }, undefined, []);
  assert.deepEqual(pts, [[200, 0]]);

  // dy=200 > dx=50 -> vertical leg first, corner at (start.x, end.y).
  const pts2 = suggestRoute({ x: 0, y: 0 }, undefined, { x: 50, y: 200 }, undefined, []);
  assert.deepEqual(pts2, [[0, 200]]);
});

test("directional ends: the route's first/last interior vertex is the PORT_STUB stub, not the bare port point", () => {
  const pts = suggestRoute({ x: 0, y: 0 }, "right", { x: 200, y: 0 }, "left", []);
  // start exits right (+x), end is entered from the left (also +x vector,
  // same convention withDirStub() uses) -> stubs at x=12 and x=188.
  assert.deepEqual(pts, [[12, 0], [188, 0]]);
});

test("an obstacle directly between the ports: canonical L/Z shapes all cross it, falls back to hugging one of its edges", () => {
  const obstacle: ObstacleRect = { x: 80, y: 0, w: 40, h: 100 };
  const pts = suggestRoute({ x: 0, y: 50 }, undefined, { x: 200, y: 50 }, undefined, [obstacle]);
  // Every straight/L/Z shape between two same-y ports degenerates to the
  // same blocked y=50 line through the obstacle; hugging its top edge
  // (y=0) is the first side that actually clears (left/right hugs still
  // cross the obstacle's own x-span at y=50).
  assert.deepEqual(pts, [[0, 0], [200, 0]]);
  // And it must actually be clear of the obstacle: every leg is a
  // horizontal/vertical run that never dips into the obstacle's interior.
  const full: [number, number][] = [[0, 50], ...pts, [200, 50]];
  for (let i = 0; i < full.length - 1; i++) {
    const [x1, y1] = full[i];
    const [x2, y2] = full[i + 1];
    const crossesX = Math.min(x1, x2) < obstacle.x + obstacle.w && Math.max(x1, x2) > obstacle.x;
    const crossesY = Math.min(y1, y2) < obstacle.y + obstacle.h && Math.max(y1, y2) > obstacle.y;
    assert.ok(!(crossesX && crossesY), `segment ${i} (${x1},${y1})->(${x2},${y2}) crosses the obstacle`);
  }
});

test("several staggered obstacles: still finds a clear path (whichever fallback tier it took)", () => {
  // Three obstacles staggered between the ports — none of the individual
  // canonical/hug shapes are assumed here; this only asserts the actual
  // output (from whichever tier resolved it) is fully obstacle-clear.
  const obstacles: ObstacleRect[] = [
    { x: 50, y: -50, w: 20, h: 100 },
    { x: 90, y: -100, w: 20, h: 140 },
    { x: 130, y: -50, w: 20, h: 100 }
  ];
  const start = { x: 0, y: 0 };
  const end = { x: 180, y: 0 };
  const pts = suggestRoute(start, undefined, end, undefined, obstacles);
  assert.ok(pts.length > 0, "expected a non-trivial detour");
  const full: [number, number][] = [[start.x, start.y], ...pts, [end.x, end.y]];
  for (let i = 0; i < full.length - 1; i++) {
    const [x1, y1] = full[i];
    const [x2, y2] = full[i + 1];
    for (const o of obstacles) {
      const crossesX = Math.min(x1, x2) < o.x + o.w && Math.max(x1, x2) > o.x;
      const crossesY = Math.min(y1, y2) < o.y + o.h && Math.max(y1, y2) > o.y;
      assert.ok(!(crossesX && crossesY), `segment (${x1},${y1})->(${x2},${y2}) crosses obstacle ${JSON.stringify(o)}`);
    }
  }
});

test("last-resort fallback: both ports buried inside one giant obstacle — no shape or search can clear it, so the plain L is returned anyway (never throws, never returns nothing)", () => {
  const obstacle: ObstacleRect = { x: -1000, y: -1000, w: 2000, h: 2000 };
  const pts = suggestRoute({ x: 0, y: 0 }, undefined, { x: 500, y: 500 }, undefined, [obstacle]);
  // The plain L (dominant axis first, same shape canonicalShapes would
  // have tried first) — used unconditionally as the absolute last resort.
  assert.deepEqual(pts, [[500, 0]]);
});

test("deterministic: identical inputs always produce the identical route", () => {
  const obstacles: ObstacleRect[] = [{ x: 80, y: 0, w: 40, h: 100 }];
  const a = suggestRoute({ x: 0, y: 50 }, undefined, { x: 200, y: 50 }, undefined, obstacles);
  const b = suggestRoute({ x: 0, y: 50 }, undefined, { x: 200, y: 50 }, undefined, obstacles);
  assert.deepEqual(a, b);
});

// ── ends: a route leaves its own equipment before going around it ─────────
const M = 16;
const grow = (r: ObstacleRect): ObstacleRect => ({ x: r.x - M, y: r.y - M, w: r.w + 2 * M, h: r.h + 2 * M });
/** True when the axis-aligned segment a->b enters the strict interior of r. */
function crosses(a: [number, number], b: [number, number], r: ObstacleRect): boolean {
  const [x0, x1] = [Math.min(a[0], b[0]), Math.max(a[0], b[0])];
  const [y0, y1] = [Math.min(a[1], b[1]), Math.max(a[1], b[1])];
  return x1 > r.x && x0 < r.x + r.w && y1 > r.y && y0 < r.y + r.h;
}
// A built-in Tank (220 x 260 at x 400) with its right port on the vessel
// wall — inside its own box, as the default ports now are.
const tank: ObstacleRect = { x: 400, y: 0, w: 220, h: 260 };
const tankRight = { x: 586, y: 121 };

test("ends: a side port's route exits its own equipment outward, then goes around it (never back through the vessel)", () => {
  // A valve down and to the LEFT of the tank: the L that ignores the tank
  // body (what re-route produced) runs straight back through it.
  const valve: ObstacleRect = { x: 200, y: 360, w: 90, h: 80 };
  const valveIn = { x: 212, y: 404 };
  const obstacles = [grow(tank), grow(valve)];
  const pts = suggestRoute(tankRight, "right", valveIn, "left", obstacles, { start: grow(tank), end: grow(valve) });
  // first vertex: straight out of the right wall, past the tank's keep-out
  assert.deepEqual(pts[0], [tank.x + tank.w + M, tankRight.y]);
  const full: [number, number][] = [[tankRight.x, tankRight.y], ...pts, [valveIn.x, valveIn.y]];
  // every leg after the exit clears the tank body; the last leg enters the valve from its left face
  for (let i = 1; i < full.length - 1; i++) {
    assert.equal(crosses(full[i], full[i + 1], tank), false, `leg ${i} ${JSON.stringify([full[i], full[i + 1]])} crosses the tank`);
  }
  assert.deepEqual(pts[pts.length - 1], [valve.x - M, valveIn.y]);
});

test("ends: with the old exclusion (no end boxes, tank not an obstacle) the same route cuts through the tank", () => {
  const valveIn = { x: 212, y: 404 };
  const pts = suggestRoute(tankRight, "right", valveIn, "left", []);
  const full: [number, number][] = [[tankRight.x, tankRight.y], ...pts, [valveIn.x, valveIn.y]];
  const legs = full.slice(1, -1).map((p, i) => [p, full[i + 2]] as const);
  assert.ok(legs.some(([a, b]) => crosses(a, b, tank)));
});

test("ends: a port with no dir exits through the face it sits nearest (faceDir)", () => {
  assert.equal(faceDir({ x: 412, y: 121 }, tank), "left");
  assert.equal(faceDir({ x: 510, y: 250 }, tank), "down");
  assert.equal(faceDir({ x: 510, y: 5 }, tank), "up");
  assert.equal(faceDir(tankRight, tank), "right");
  const pts = suggestRoute({ x: 412, y: 121 }, undefined, { x: 100, y: 121 }, undefined, [grow(tank)], { start: grow(tank) });
  assert.deepEqual(pts, [[tank.x - M, 121]]);
});

test("ends: the exit is never shorter than the PORT_STUB leg", () => {
  // a port right on its (unexpanded) box edge with a zero-margin box
  const box: ObstacleRect = { x: 0, y: 0, w: 100, h: 100 };
  const pts = suggestRoute({ x: 100, y: 50 }, "right", { x: 300, y: 50 }, undefined, [box], { start: box });
  assert.ok(pts[0][0] >= 100 + PORT_STUB);
});

test("directional ends: of two clear Ls, the one that continues out of both ports (fewest bends counting the port turns)", () => {
  // A pump outlet exiting UP at (100, 300), a tank nozzle exiting LEFT at
  // (400, 100): dx > dy, so the dominant-axis L alone would run horizontal
  // first — turning right straight off the outlet and again at the nozzle
  // (a staircase, three bends). Vertical-first continues up out of the
  // pump and turns once into the nozzle.
  const pts = suggestRoute({ x: 100, y: 300 }, "up", { x: 400, y: 100 }, "left", []);
  const s = PORT_STUB;
  assert.deepEqual(pts, [[100, 300 - s], [100, 100], [400 - s, 100]]);
});
