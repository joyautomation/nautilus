// Plain-Node tests for the `naut compose` contract (composeCli.ts).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { composeArgs, composeTooOldMessage, composeUnsupported, overridesFor, parseComposeOutput } from "./composeCli";

const OK = JSON.stringify({
  root: "/p",
  prelude: "FUNCTION_BLOCK MotorStarter\nEND_FUNCTION_BLOCK\n",
  libraries: ["pump.st", "motor.ld"],
  programs: [{ file: "permissives.ld", pou: "Permissives", language: "ld", program: "PROGRAM Permissives\nEND_PROGRAM\n" }],
});

test("composeArgs asks for JSON with buffers on stdin", () => {
  assert.deepEqual(composeArgs("/p/permissives.ld"), ["compose", "--json", "--overrides", "-", "/p/permissives.ld"]);
});

test("parseComposeOutput: a clean exit is the composition", () => {
  const r = parseComposeOutput(null, OK, "", "0.13.0");
  assert.ok("ok" in r);
  assert.deepEqual(r.ok.libraries, ["pump.st", "motor.ld"]);
  assert.equal(r.ok.programs[0].pou, "Permissives");
});

test("parseComposeOutput: garbage on a clean exit is an error, not a crash", () => {
  const r = parseComposeOutput(null, "not json", "", "0.13.0");
  assert.ok("error" in r);
});

test("parseComposeOutput: the CLI's own message comes through", () => {
  const err = Object.assign(new Error("exit 1"), { code: 1 });
  const r = parseComposeOutput(err, "", "naut compose: lib/extra.st declares a PROGRAM, but lib/ holds libraries only — programs belong in the root and in `tasks:`\n", "0.13.0");
  assert.ok("error" in r);
  assert.equal(r.error, "nautilus: naut compose: lib/extra.st declares a PROGRAM, but lib/ holds libraries only — programs belong in the root and in `tasks:`");
  assert.equal(r.tooOld, undefined);
});

test("parseComposeOutput: a naut without `compose` says to update", () => {
  const stderr = 'nautilus: unknown command "compose"\n\nnautilus — SCADA, built like software\n\nUsage:\n  naut lsp ...\n';
  assert.equal(composeUnsupported(stderr), true);
  const r = parseComposeOutput(Object.assign(new Error("exit 2"), { code: 2 }), "", stderr, "0.13.0");
  assert.ok("error" in r);
  assert.equal(r.tooOld, true);
  assert.equal(r.error, composeTooOldMessage("0.13.0"));
  assert.match(r.error, /naut 0\.13\.0 or newer/);
  assert.match(r.error, /Install or Update the naut CLI/);
});

test("parseComposeOutput: a timeout says so", () => {
  const r = parseComposeOutput(Object.assign(new Error("killed"), { killed: true }), "", "", "0.13.0");
  assert.ok("error" in r);
  assert.match(r.error, /timed out/);
});

test("overridesFor sends only dirty files, by absolute path", () => {
  assert.deepEqual(
    overridesFor([
      { fsPath: "/p/lib/motor.ld", text: "edited", dirty: true, isFile: true },
      { fsPath: "/p/pump.st", text: "saved", dirty: false, isFile: true },
      { fsPath: "Untitled-1", text: "scratch", dirty: true, isFile: false },
    ]),
    { "/p/lib/motor.ld": "edited" }
  );
});
