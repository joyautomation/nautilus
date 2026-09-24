/// <reference types="node" />
// Regression coverage for BUG 1 (Enter wouldn't complete a pipe with an
// anchored end) — see EditorCanvas.svelte's finishPipe(), which delegates
// to resolveDraftFinish() below. No svelte/vscode imports, runs directly
// with `node --experimental-strip-types --test` like the other mimic
// webview pure-logic tests.
import { strict as assert } from "node:assert";
import { test } from "node:test";
import { minInteriorPoints, resolveDraftFinish, type NamedPort } from "./pipeDraft.ts";

// `hoverPort`/`cursor` carry the port's resolved position (as the real
// EditorCanvas caller does); `draftFrom`/`draftLastSnap` are the already-
// STRIPPED {equip, port} anchor refs that end up on the wire (as
// canvasDown's own `named ? { equip: named.equip, port: named.port } : null`
// already does) — kept separate here so the assertions below can compare
// the anchor fields exactly, with no leftover x/y.
const portA: NamedPort = { equip: "tank1", port: "left", x: 100, y: 100 };
const portB: NamedPort = { equip: "tank2", port: "right", x: 300, y: 100 };
const anchorA = { equip: portA.equip, port: portA.port };
const anchorB = { equip: portB.equip, port: portB.port };

test("minInteriorPoints: floor drops by one per anchored end, never below zero", () => {
  assert.equal(minInteriorPoints(false, false), 2);
  assert.equal(minInteriorPoints(true, false), 1);
  assert.equal(minInteriorPoints(false, true), 1);
  assert.equal(minInteriorPoints(true, true), 0);
});

test("floating pipe: two explicit clicks finish exactly as clicked, cursor drift ignored", () => {
  const draft: [number, number][] = [[10, 10], [50, 10]];
  // Cursor has since wandered off somewhere else entirely — must NOT be
  // appended as a spurious third point once the draft alone already meets
  // the (anchor-free) floor of 2.
  const result = resolveDraftFinish(draft, null, null, [999, 999], null);
  assert.deepEqual(result, { points: [[10, 10], [50, 10]] });
});

test("BUG 1 regression: port A clicked, port B only hovered (no click) — Enter completes the zero-interior pipe", () => {
  const draft: [number, number][] = [[portA.x, portA.y]];
  const result = resolveDraftFinish(draft, anchorA, null, [portB.x, portB.y], portB);
  assert.deepEqual(result, {
    points: [],
    from: { equip: "tank1", port: "left" },
    to: { equip: "tank2", port: "right" }
  });
});

test("both ports explicitly clicked: zero-interior pipe completes with no cursor involvement", () => {
  const draft: [number, number][] = [[portA.x, portA.y], [portB.x, portB.y]];
  const result = resolveDraftFinish(draft, anchorA, anchorB, null, null);
  assert.deepEqual(result, {
    points: [],
    from: { equip: "tank1", port: "left" },
    to: { equip: "tank2", port: "right" }
  });
});

test("one end anchored, one floating: an interior point plus the anchor click is enough", () => {
  const draft: [number, number][] = [[portA.x, portA.y], [200, 150]];
  const result = resolveDraftFinish(draft, anchorA, null, [200, 150], null);
  assert.deepEqual(result, {
    points: [[200, 150]],
    from: { equip: "tank1", port: "left" }
  });
});

test("anchored end with only the anchor click and no cursor at all: not enough yet (null)", () => {
  const draft: [number, number][] = [[portA.x, portA.y]];
  assert.equal(resolveDraftFinish(draft, anchorA, null, null, null), null);
});

test("nothing drawn yet: Enter is a no-op regardless of cursor", () => {
  assert.equal(resolveDraftFinish([], null, null, [50, 50], null), null);
  assert.equal(resolveDraftFinish([], null, null, null, null), null);
});

test("floating single click + hover + Enter also completes (symmetry: the cursor stand-in isn't anchor-only)", () => {
  const draft: [number, number][] = [[10, 10]];
  const result = resolveDraftFinish(draft, null, null, [80, 10], null);
  assert.deepEqual(result, { points: [[10, 10], [80, 10]] });
});

test("floating start click, then hover a port and press Enter without clicking it", () => {
  const draft: [number, number][] = [[10, 10]];
  const result = resolveDraftFinish(draft, null, null, [portB.x, portB.y], portB);
  assert.deepEqual(result, {
    points: [[10, 10]],
    to: { equip: "tank2", port: "right" }
  });
});

test("BUG 3 regression: bent pipe ending on a hovered port connects even though the clicked draft already meets the floor", () => {
  // click port A, click a corner (NOT a port, so draftLastSnap stays null),
  // then Enter while resting on port B. The draft alone ([A, corner]) already
  // meets the floor for a one-anchor pipe, but the user is plainly aiming the
  // END at port B (the rubber band has been showing that segment) — dropping
  // it was the "Enter won't complete the pipe TO the port" bug.
  const corner: [number, number] = [portA.x, 40];
  const draft: [number, number][] = [[portA.x, portA.y], corner];
  const result = resolveDraftFinish(draft, anchorA, null, [portB.x, portB.y], portB);
  assert.deepEqual(result, {
    points: [corner],
    from: { equip: "tank1", port: "left" },
    to: { equip: "tank2", port: "right" }
  });
});

test("BUG 3 regression: two floating clicks then Enter over a port connects the end (port not dropped)", () => {
  // Two ordinary clicks already meet the floating floor of 2; the user then
  // moves the end onto a port and presses Enter. The hovered port must become
  // the `to` anchor — not silently dropped, leaving a stub short of it.
  const draft: [number, number][] = [[10, 10], [50, 10]];
  const result = resolveDraftFinish(draft, null, null, [portB.x, portB.y], portB);
  assert.deepEqual(result, {
    points: [[10, 10], [50, 10]],
    to: { equip: "tank2", port: "right" }
  });
});

test("floating drift over EMPTY canvas on a complete draft is still ignored (no spurious point)", () => {
  // Same two clicks, but the cursor is over empty space (no hoverPort) — the
  // rubber-band end is NOT committed; the pipe finishes exactly as clicked.
  const draft: [number, number][] = [[10, 10], [50, 10]];
  const result = resolveDraftFinish(draft, null, null, [400, 400], null);
  assert.deepEqual(result, { points: [[10, 10], [50, 10]] });
});

test("draft already meets the floor via clicks alone: a hovered port is NOT retroactively substituted in", () => {
  // Both ends already anchored by explicit clicks — even though the cursor
  // is currently sitting somewhere else (or even on a third port), the
  // already-complete draft finishes with exactly what was clicked.
  const draft: [number, number][] = [[portA.x, portA.y], [portB.x, portB.y]];
  const thirdPort: NamedPort = { equip: "pump1", port: "in", x: 500, y: 500 };
  const result = resolveDraftFinish(draft, anchorA, anchorB, [500, 500], thirdPort);
  assert.deepEqual(result, {
    points: [],
    from: { equip: "tank1", port: "left" },
    to: { equip: "tank2", port: "right" }
  });
});
