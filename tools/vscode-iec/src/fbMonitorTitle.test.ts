import { test } from "node:test";
import assert from "node:assert/strict";
import { fbMonitorTitle } from "./fbMonitorTitle";

test("no monitor chosen", () => {
  assert.equal(fbMonitorTitle("", ["roc", "roc2"]), "○ live values: monitor an instance…");
});

test("a single instance has no position", () => {
  assert.equal(fbMonitorTitle("roc", ["roc"]), "◉ live values: monitoring roc");
});

test("the position is the monitored instance's real index", () => {
  assert.equal(fbMonitorTitle("roc", ["roc", "roc2"]), "◉ live values: monitoring roc — 1 of 2, click to switch");
  assert.equal(fbMonitorTitle("roc2", ["roc", "roc2"]), "◉ live values: monitoring roc2 — 2 of 2, click to switch");
});

test("an instance missing from the candidates falls back to a count", () => {
  assert.equal(fbMonitorTitle("gone", ["a", "b", "c"]), "◉ live values: monitoring gone — 3 instances, click to switch");
});
