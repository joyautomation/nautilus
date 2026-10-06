import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  describesLanguage,
  findIdentifier,
  isXrefMessage,
  maskNonCode,
  normalizeDescriptions,
  xrefPath,
} from "./xrefTarget";

const LD = [
  "PROGRAM Conveyor", //                                   1
  "VAR_EXTERNAL", //                                       2
  "    M1_Run : BOOL; (* M1_Run: motor 1 running *)", //  3
  "    M1_StartPB : BOOL;", //                            4
  "END_VAR", //                                            5
  "RUNG m1 (* start M1_Run on M1_StartPB *):", //         6
  "    +M1_StartPB m1(Stop := M1_StopPB) ( M1_Run )", //  7
  "RUNG m2perm:", //                                       8
  "    M1_Run [ m1.FailToStart | Maint ] ( M2_Permit )", // 9
  "END_PROGRAM", //                                       10
].join("\n");

test("isXrefMessage: a name is required; lines are optional numbers", () => {
  assert.ok(isXrefMessage({ type: "xref", name: "M1_Run", line: 6, endLine: 7 }));
  assert.ok(isXrefMessage({ type: "xref", name: "M1_Run" }));
  assert.ok(!isXrefMessage({ type: "xref", name: "  " }));
  assert.ok(!isXrefMessage({ type: "xref", name: "M1_Run", line: "6" }));
  assert.ok(!isXrefMessage({ type: "ldEdit", name: "M1_Run" }));
  assert.ok(!isXrefMessage(null));
});

test("xrefPath: edge marks, NOT and indexes drop; members stay; expressions are not identifiers", () => {
  assert.deepEqual(xrefPath("+M1_StartPB"), ["M1_StartPB"]);
  assert.deepEqual(xrefPath("-Fault"), ["Fault"]);
  assert.deepEqual(xrefPath("NOT Fault"), ["Fault"]);
  assert.deepEqual(xrefPath("Levels[i]"), ["Levels"]);
  assert.deepEqual(xrefPath("a[b[1]].c"), ["a", "c"]);
  assert.deepEqual(xrefPath("t1.Q"), ["t1", "Q"]);
  assert.equal(xrefPath("T#5S"), undefined);
  assert.equal(xrefPath("A > B"), undefined);
  assert.equal(xrefPath("42"), undefined);
});

test("maskNonCode blanks comments, pragmas and strings, keeping offsets and newlines", () => {
  const src = "a (* b\nc *) d // e\nf 'g' \"h\" i";
  const m = maskNonCode(src);
  assert.equal(m.length, src.length);
  assert.equal(m.split("\n").length, 3);
  assert.deepEqual(m.match(/[a-z]/g), ["a", "d", "f", "i"]);
});

test("findIdentifier: inside the element's own lines, never in a comment", () => {
  // The rung m2perm (lines 8–9): its M1_Run, not the VAR line's or rung m1's.
  assert.deepEqual(findIdentifier(LD, "M1_Run", { line: 8, endLine: 9 }), { line: 8, character: 4 });
  // Rung m1 (6–7): the coil's M1_Run — the rung comment naming it is skipped.
  assert.deepEqual(findIdentifier(LD, "M1_Run", { line: 6, endLine: 7 }), { line: 6, character: 40 });
  // An edge contact's label carries its +.
  assert.deepEqual(findIdentifier(LD, "+M1_StartPB", { line: 6, endLine: 7 }), { line: 6, character: 5 });
});

test("findIdentifier: no hint, or a hint that misses, takes the first occurrence in code", () => {
  assert.deepEqual(findIdentifier(LD, "M1_Run"), { line: 2, character: 4 });
  assert.deepEqual(findIdentifier(LD, "M1_Run", { line: 1, endLine: 1 }), { line: 2, character: 4 });
  // Case-insensitive, as IEC is.
  assert.deepEqual(findIdentifier(LD, "m1_run", { line: 8, endLine: 9 }), { line: 8, character: 4 });
  assert.equal(findIdentifier(LD, "Nowhere"), undefined);
  assert.equal(findIdentifier(LD, "T#5S"), undefined);
});

test("findIdentifier: a member lands on its last segment; a prefix or another member does not match", () => {
  assert.deepEqual(findIdentifier(LD, "m1.FailToStart"), { line: 8, character: 16 });
  // `m1` alone is the instance rung m1 calls — not the rung's own name m1,
  // and not M1_StartPB, which merely starts with the same letters.
  assert.deepEqual(findIdentifier(LD, "m1", { line: 6, endLine: 7 }), { line: 6, character: 16 });
  assert.equal(findIdentifier(LD, "m1.Other"), undefined);
  // The Q of `t1 . Q[2]`-style spacing and indexes between segments.
  assert.deepEqual(findIdentifier("x := arr[i] . v;", "arr[3].v"), { line: 0, character: 14 });
});

test("normalizeDescriptions lower-cases names and drops empty ones", () => {
  assert.deepEqual(
    normalizeDescriptions({ descriptions: { M1_StartPB: " M1 start pushbutton ", Blank: "", N: 3 } }),
    { m1_startpb: "M1 start pushbutton" }
  );
  assert.deepEqual(normalizeDescriptions(null), {});
  assert.deepEqual(normalizeDescriptions({}), {});
});

test("describesLanguage: the three diagram languages only", () => {
  assert.ok(describesLanguage("iec-ld") && describesLanguage("iec-fbd") && describesLanguage("iec-sfc"));
  assert.ok(!describesLanguage("iec-st") && !describesLanguage("logix-l5x"));
});
