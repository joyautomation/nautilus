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
  firstFreeSlot,
  nextPortName,
  newPortAtFreeSlot,
  newPortAtPoint,
  nudgePort,
  roundPort,
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
