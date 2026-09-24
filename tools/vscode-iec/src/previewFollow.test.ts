import { strict as assert } from "node:assert";
import { test } from "node:test";
import { followActiveDoc } from "./previewFollow";

const base = { src: "PROGRAM p END_PROGRAM", title: "a.fbd — HEAD ↔ working tree" };

test("re-focusing the SAME document keeps the diff (vs HEAD, controller, or revisions)", () => {
  const next = followActiveDoc({ docUri: "file:///p/a.fbd", diffBase: base }, "file:///p/a.fbd");
  assert.equal(next.docUri, "file:///p/a.fbd");
  assert.equal(next.diffBase, base);
  const frozen = { ...base, headSrc: "PROGRAM p END_PROGRAM" };
  assert.equal(followActiveDoc({ docUri: "file:///p/a.fbd", diffBase: frozen }, "file:///p/a.fbd").diffBase, frozen);
});

test("switching to a DIFFERENT document leaves diff mode (never diffs B against A's base)", () => {
  const next = followActiveDoc({ docUri: "file:///p/a.ld", diffBase: base }, "file:///p/b.ld");
  assert.equal(next.docUri, "file:///p/b.ld");
  assert.equal(next.diffBase, undefined);
});

test("no tracked document yet: follow it, no diff", () => {
  assert.deepEqual(followActiveDoc({}, "file:///p/a.sfc"), { docUri: "file:///p/a.sfc", diffBase: undefined });
});
