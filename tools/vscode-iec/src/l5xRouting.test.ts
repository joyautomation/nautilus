// The ladder view serves two completely different kinds of file — nautilus
// .ld source and Rockwell .L5X exports — and the ONLY thing that
// distinguishes them downstream is which CLI verb produced the model. Get
// this dispatch wrong and an L5X is handed to `nautilus ld graph`, which
// reports a parse error on XML and looks like a broken reader.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import { graphArgs, isL5X } from "./l5xRouting";

test("isL5X recognises the export regardless of case", () => {
  for (const p of ["/w/DemoLine.L5X", "/w/demoline.l5x", "C:\\w\\A.L5x"]) {
    assert.equal(isL5X(p), true, p);
  }
  for (const p of ["/w/main.ld", "/w/main.st", "/w/l5x.ld", undefined]) {
    assert.equal(isL5X(p), false, String(p));
  }
});

test("an L5X routes to `logix graph`, nautilus source to `ld graph`", () => {
  assert.deepEqual(graphArgs("/w/DemoLine.L5X"), ["logix", "graph", "-"]);
  assert.deepEqual(graphArgs("/w/main.ld"), ["ld", "graph", "-", "/w/main.ld"]);
});

// An untitled buffer has no path; it is nautilus source by definition
// (an L5X is always a file on disk), so it must not lose the ld verb.
test("no path still graphs as nautilus ladder", () => {
  assert.deepEqual(graphArgs(undefined), ["ld", "graph", "-"]);
});

// The L5X form takes no trailing path: `logix graph -` reads the whole
// controller from stdin, and a second argument there is a ROUTINE
// selector, so passing the file path would silently select nothing.
test("the L5X form never passes a path as a routine selector", () => {
  assert.equal(graphArgs("/w/DemoLine.L5X").length, 3);
});
