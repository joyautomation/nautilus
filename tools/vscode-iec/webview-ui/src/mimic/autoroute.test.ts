/// <reference types="node" />
// Coverage for the route-suggestion generator (autoroute.ts): shape
// selection order, obstacle avoidance, determinism, and PORT_STUB
// compliance at directional ends. Like routing.test.ts, runs directly with
// `node --experimental-strip-types --test` — no svelte/vscode imports.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { suggestRoute, type ObstacleRect } from "./autoroute.ts";

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
