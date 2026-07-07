// Plain-Node tests for the identifier scanner (no vscode dependency).
// Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { scanIdentifiers, formatValue } from "./scan";

const values = new Map<string, unknown>([
  ["levelpct", 59.887482],
  ["pumprun", true],
  ["tempsp", 65],
]);

test("finds identifiers case-insensitively", () => {
  const sites = scanIdentifiers("IF levelPct < 40.0 THEN PumpRun := TRUE; END_IF;", values);
  assert.deepEqual(
    sites.map((s) => s.lowerName),
    ["levelpct", "pumprun"]
  );
});

test("end offset lands just past the identifier", () => {
  const text = "TempSP := 65.0;";
  const [site] = scanIdentifiers(text, values);
  assert.equal(text.slice(0, site.end), "TempSP");
});

test("skips line comments, block comments, and strings", () => {
  const text = [
    "// LevelPct in a line comment",
    "(* PumpRun in a block comment *)",
    "msg := 'LevelPct in a string';",
    'msg2 := "TempSP too";',
    "LevelPct := 1.0;",
  ].join("\n");
  const sites = scanIdentifiers(text, values);
  assert.deepEqual(
    sites.map((s) => s.lowerName),
    ["levelpct"]
  );
});

test("does not match partial words", () => {
  const sites = scanIdentifiers("LevelPctX := LevelPct2;", values);
  assert.equal(sites.length, 0);
});

test("formatValue renders compactly", () => {
  assert.equal(formatValue(59.887482), "59.887");
  assert.equal(formatValue(65), "65");
  assert.equal(formatValue(true), "TRUE");
  assert.equal(formatValue(false), "FALSE");
  assert.equal(formatValue(null), "—");
  assert.equal(formatValue(1e9), "1000000000"); // integers never go exponential
  assert.equal(formatValue(1234567.89), "1.23e+6");
});
