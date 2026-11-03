/// <reference types="node" />
// Pure-math coverage for the shared ports gesture helpers — no svelte/vscode
// imports, so (like mimicOps.test.ts/mimicComponentIndex.test.ts on the
// extension-host side) it runs directly with `node --experimental-strip-types
// --test` over this file. Not wired into the extension's `npm test` (this
// package has no build/dependencies of its own to compile that way) — run
// ad hoc: `node --experimental-strip-types --test src/mimic/portsGestures.test.ts`
// from webview-ui/.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  dropPort,
  firstFreeSlot,
  movePort,
  nextPortName,
  newPortAtFreeSlot,
  SLOT_CLEARANCE,
  newPortAtPoint,
  nudgePort,
  roundPort,
  smallestPartAt,
  type Port,
} from "./portsGestures.ts";

test("nextPortName picks p1, p2, ... skipping names already taken", () => {
  assert.equal(nextPortName([]), "p1");
  assert.equal(nextPortName([{ name: "tubeIn", x: 0, y: 0 }]), "p1");
  assert.equal(nextPortName([{ name: "p1", x: 0, y: 0 }]), "p2");
  assert.equal(
    nextPortName([
      { name: "p1", x: 0, y: 0 },
      { name: "p2", x: 0, y: 0 },
    ]),
    "p3"
  );
  // A gap (p2 free, p3 taken) still fills the first free slot, not the end.
  assert.equal(
    nextPortName([
      { name: "p1", x: 0, y: 0 },
      { name: "p3", x: 0, y: 0 },
    ]),
    "p2"
  );
});

test("firstFreeSlot scans edge midpoints before quarters, skipping occupied fractions", () => {
  assert.deepEqual(firstFreeSlot([]), [0.5, 0]); // top midpoint first
  assert.deepEqual(firstFreeSlot([{ name: "a", x: 0.5, y: 0 }]), [1, 0.5]); // -> right midpoint
  assert.deepEqual(
    firstFreeSlot([
      { name: "a", x: 0.5, y: 0 },
      { name: "b", x: 1, y: 0.5 },
      { name: "c", x: 0.5, y: 1 },
      { name: "d", x: 0, y: 0.5 },
    ]),
    [0.25, 0] // all four midpoints taken -> first quarter slot
  );
});

test("newPortAtFreeSlot auto-names AND places at the first free slot together", () => {
  const existing: Port[] = [{ name: "p1", x: 0.5, y: 0 }];
  assert.deepEqual(newPortAtFreeSlot(existing), { name: "p2", x: 1, y: 0.5 });
});

test("newPortAtPoint projects onto the nearer edge, quarter-snaps, and auto-names", () => {
  const box = { x: 0, y: 0, w: 100, h: 100 };
  const p = newPortAtPoint(box, [96, 40], []); // near the right edge
  assert.deepEqual(p, { name: "p1", x: 1, y: 0.5 });
});

test("roundPort rounds fractions to 3 decimals and keeps the name", () => {
  assert.deepEqual(roundPort({ name: "tubeIn", x: 0.123456, y: 0.987654 }), { name: "tubeIn", x: 0.123, y: 0.988 });
});

test("nudgePort steps a port by canvas pixels converted to the box's fraction space", () => {
  const box = { x: 0, y: 0, w: 100, h: 200 };
  const port: Port = { name: "p1", x: 0.5, y: 0.5 };
  assert.deepEqual(nudgePort(box, port, 1, 0), { name: "p1", x: 0.51, y: 0.5 });
  assert.deepEqual(nudgePort(box, port, -1, 0), { name: "p1", x: 0.49, y: 0.5 });
  assert.deepEqual(nudgePort(box, port, 0, 10), { name: "p1", x: 0.5, y: 0.55 }); // Shift-arrow's GRID-pixel step
  assert.deepEqual(nudgePort(box, port, 0, -10), { name: "p1", x: 0.5, y: 0.45 });
});

test("nudgePort clamps at the box edge like a drag does, and keeps dir", () => {
  const box = { x: 0, y: 0, w: 100, h: 100 };
  assert.deepEqual(nudgePort(box, { name: "right", x: 1, y: 0.5 }, 5, 0), { name: "right", x: 1, y: 0.5 });
  assert.deepEqual(nudgePort(box, { name: "left", x: 0, y: 0.5 }, -5, 0), { name: "left", x: 0, y: 0.5 });
  assert.deepEqual(nudgePort(box, { name: "p1", x: 0.5, y: 0.5, dir: "up" }, 1, 0), { name: "p1", x: 0.51, y: 0.5, dir: "up" });
});

test("nudgePort composes with roundPort exactly like the drag-then-commit path", () => {
  const box = { x: 0, y: 0, w: 300, h: 300 };
  const nudged = nudgePort(box, { name: "p1", x: 0.5, y: 0.5 }, 1, 0);
  assert.deepEqual(roundPort(nudged), { name: "p1", x: 0.503, y: 0.5 });
});

// ── part-anchored ports: dropping re-anchors to the part under the cursor ──
const BOX = { x: 100, y: 100, w: 60, h: 100 };
const PARTS: [string, { x: number; y: number; w: number; h: number }][] = [
  ["motor", { x: 110, y: 110, w: 30, h: 60 }],
  ["flange", { x: 145, y: 150, w: 12, h: 2 }],
];

test("smallestPartAt: the smallest part under the point wins; a thin part has slop", () => {
  assert.equal(smallestPartAt(PARTS, [120, 120])?.[0], "motor");
  // 3px above a 2px-tall flange still hits it
  assert.equal(smallestPartAt(PARTS, [150, 147])?.[0], "flange");
  assert.equal(smallestPartAt(PARTS, [102, 195]), null);
});

test("dropPort: on a part, fractions of that part; off every part, of the box", () => {
  assert.deepEqual(dropPort(BOX, PARTS, [125, 110]), { x: 0.5, y: 0, part: "motor" });
  assert.deepEqual(dropPort(BOX, PARTS, [130, 200]), { x: 0.5, y: 1 });
});

test("movePort: replaces x/y/part, keeps name and dir, drops a stale part", () => {
  const p: Port = { name: "discharge", x: 0.5, y: 0, part: "flange", dir: "up" };
  assert.deepEqual(movePort(p, { x: 0.2, y: 1 }), { name: "discharge", x: 0.2, y: 1, dir: "up" });
  assert.deepEqual(movePort(p, { x: 0, y: 0.5, part: "motor" }), { name: "discharge", x: 0, y: 0.5, dir: "up", part: "motor" });
});

// nautilus#477: a slot is taken by a dot that renders on it, not only by a
// port at exactly its fractions.
test("firstFreeSlot: a port a few pixels off a slot takes it (the rig's Pump)", () => {
  const box = { x: 0, y: 0, w: 120, h: 110 };
  // Pump's `out` at the host's old defaults: ~2 px across, ~6 px down from top-mid.
  const out: Port = { name: "out", x: 0.483, y: 0.055 };
  assert.deepEqual(firstFreeSlot([out], box), [1, 0.5]);
  assert.deepEqual(newPortAtFreeSlot([out], box), { name: "p1", x: 1, y: 0.5 });
});

test("firstFreeSlot: judged in the part's frame where a port names a part", () => {
  const box = { x: 0, y: 0, w: 100, h: 100 };
  const parts: [string, typeof box][] = [
    ["nozzle", { x: 40, y: 0, w: 20, h: 10 }],
    ["body", { x: 20, y: 40, w: 60, h: 40 }],
  ];
  // Top-mid of the nozzle renders at (50, 0): the box's top-mid slot.
  assert.deepEqual(firstFreeSlot([{ name: "n", x: 1, y: 0.5, part: "nozzle" }], box, parts), [1, 0.5]);
  assert.deepEqual(firstFreeSlot([{ name: "n", x: 0.5, y: 0, part: "nozzle" }], box, parts), [1, 0.5]);
  // Top-mid of the body has the same fractions but renders at (50, 40),
  // well inside the box: the box's top-mid slot is still free.
  assert.deepEqual(firstFreeSlot([{ name: "b", x: 0.5, y: 0, part: "body" }], box, parts), [0.5, 0]);
});

test("firstFreeSlot: a port well clear of every slot leaves them all free", () => {
  const box = { x: 10, y: 20, w: 200, h: 100 };
  assert.deepEqual(firstFreeSlot([{ name: "c", x: 0.5, y: 0.5 }], box), [0.5, 0]);
  assert.deepEqual(firstFreeSlot([{ name: "t", x: 0.45, y: 0 }], box), [1, 0.5]); // 10 px off top-mid
  assert.deepEqual(firstFreeSlot([{ name: "t", x: 0.42, y: 0 }], box), [0.5, 0]); // 16 px off: clear
});

test("firstFreeSlot: the clearance follows the zoom (dots keep their screen size)", () => {
  const box = { x: 0, y: 0, w: 200, h: 100 };
  const near: Port = { name: "t", x: 0.42, y: 0 }; // 16 render px off top-mid
  assert.deepEqual(firstFreeSlot([near], box), [0.5, 0]); // zoom 1: clear
  assert.deepEqual(firstFreeSlot([near], box, [], SLOT_CLEARANCE / 0.5), [1, 0.5]); // zoom 0.5: 16 render px is 8 on screen, taken
  assert.deepEqual(newPortAtFreeSlot([near], box, [], SLOT_CLEARANCE / 0.5), { name: "p1", x: 1, y: 0.5 });
});
