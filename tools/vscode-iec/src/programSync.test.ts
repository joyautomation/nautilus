// Plain-Node tests for the online-edit composition helpers (no vscode
// dependency). Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { controllerPrelude, inLibDir, isLibraryCandidate, normalize, pouOf, sortPaths, splitProgram } from "./programSync";

const PRELUDE = "FUNCTION_BLOCK RateOfChange\nVAR_INPUT IN : REAL; END_VAR\nEND_FUNCTION_BLOCK\n";
const BODY = "PROGRAM Main\nVAR x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n";

test("pouOf finds the PROGRAM name, in any language's program file", () => {
  assert.equal(pouOf(BODY), "Main");
  assert.equal(pouOf("(* comment *)\n  PROGRAM Interlocks\n"), "Interlocks");
  assert.equal(pouOf(PRELUDE), "");
});

test("splitProgram inverts Join for a known prelude", () => {
  assert.equal(splitProgram(PRELUDE + BODY, PRELUDE), BODY);
  // Tolerates a trailing-newline difference at the seam.
  assert.equal(splitProgram(PRELUDE.replace(/\n$/, "") + "\n" + BODY, PRELUDE), BODY);
  assert.equal(splitProgram("something else entirely\n" + BODY, PRELUDE), undefined);
});

test("controllerPrelude: deployed source carries the workspace prelude", () => {
  assert.equal(controllerPrelude(PRELUDE + BODY, PRELUDE, BODY), PRELUDE);
});

test("controllerPrelude: an online-edited prelude is peeled off the known body", () => {
  const edited = PRELUDE.replace("REAL", "LREAL");
  assert.equal(controllerPrelude(edited + BODY, PRELUDE, BODY), edited);
});

test("controllerPrelude: both halves edited — cut at the PROGRAM line", () => {
  const edited = PRELUDE.replace("REAL", "LREAL");
  const editedBody = BODY.replace("1.0", "2.0");
  assert.equal(controllerPrelude(edited + editedBody, PRELUDE, BODY), edited);
});

test("controllerPrelude: unsplittable source is returned whole", () => {
  const src = '{"nodes": []}';
  assert.equal(controllerPrelude(src, PRELUDE, BODY), src);
});

test("normalize ignores blank lines and trailing whitespace", () => {
  assert.equal(normalize("a  \n\nb\r\n"), normalize("a\nb"));
  assert.notEqual(normalize("a\nb"), normalize("a\nc"));
});

test("lib/ layout: which paths are library candidates, and in what order", () => {
  assert.equal(inLibDir("lib/motor.ld"), true);
  assert.equal(inLibDir("lib/physics/tank.st"), true);
  assert.equal(inLibDir("library.st"), false);
  assert.equal(inLibDir("hmi/lib/x.st"), false);

  assert.equal(isLibraryCandidate("pump.st"), true);
  assert.equal(isLibraryCandidate("lib/physics/tank.st"), true);
  assert.equal(isLibraryCandidate("hmi/x.st"), false);
  assert.equal(isLibraryCandidate("tags/x.st"), false);
  assert.equal(isLibraryCandidate("lib/node_modules/x.st"), false);
  assert.equal(isLibraryCandidate("lib/.cache/x.st"), false);

  // Bytewise, like Go's sort.Strings: root and lib/ interleave by path.
  assert.deepEqual(sortPaths(["types.st", "lib/z.st", "Main.st", "lib/a/b.st", "field_map.st"]), [
    "Main.st",
    "field_map.st",
    "lib/a/b.st",
    "lib/z.st",
    "types.st",
  ]);
});
