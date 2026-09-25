// Plain-Node tests for the `naut compose` contract (composeCli.ts).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import {
  composeArgs,
  composeTooOldMessage,
  composeUnsupported,
  COMPOSE_ERROR_TTL_MS,
  compositionKey,
  overridesFor,
  parseComposeOutput,
  reuseComposition,
} from "./composeCli";

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

const FILES = [
  { rel: "permissives.ld", mtime: 1, size: 10 },
  { rel: "lib/motor.ld", mtime: 2, size: 20 },
  { rel: "nautilus.yaml", mtime: 3, size: 30 },
];

test("compositionKey: same inputs in any order give the same key", () => {
  const a = compositionKey("/p/permissives.ld", "naut", FILES, [{ path: "/p/a.st", version: 1 }, { path: "/p/b.st", version: 4 }]);
  const b = compositionKey("/p/permissives.ld", "naut", [...FILES].reverse(), [{ path: "/p/b.st", version: 4 }, { path: "/p/a.st", version: 1 }]);
  assert.equal(a, b);
});

test("compositionKey: every input to composition invalidates", () => {
  const base = compositionKey("/p/permissives.ld", "naut", FILES, []);
  const touched = FILES.map((f) => (f.rel === "lib/motor.ld" ? { ...f, mtime: 9 } : f));
  const resized = FILES.map((f) => (f.rel === "lib/motor.ld" ? { ...f, size: 21 } : f));
  const variants = [
    compositionKey("/p/stats.st", "naut", FILES, []), // another target (maybe another project)
    compositionKey("/p/permissives.ld", "/opt/naut", FILES, []), // another CLI
    compositionKey("/p/permissives.ld", "naut", touched, []), // saved edit
    compositionKey("/p/permissives.ld", "naut", resized, []),
    compositionKey("/p/permissives.ld", "naut", [...FILES, { rel: "lib/pump.fbd", mtime: 1, size: 1 }], []), // file added
    compositionKey("/p/permissives.ld", "naut", FILES.slice(1), []), // file deleted
    compositionKey("/p/permissives.ld", "naut", FILES, [{ path: "/p/lib/motor.ld", version: 2 }]), // unsaved edit
  ];
  for (const v of variants) assert.notEqual(v, base);
  assert.notEqual(
    compositionKey("/p/permissives.ld", "naut", FILES, [{ path: "/p/lib/motor.ld", version: 2 }]),
    compositionKey("/p/permissives.ld", "naut", FILES, [{ path: "/p/lib/motor.ld", version: 3 }]),
    "each keystroke in an unsaved buffer"
  );
});

test("reuseComposition: a hit reuses, a miss or nothing cached recomposes", () => {
  assert.equal(reuseComposition(undefined, "k", 0), false);
  assert.equal(reuseComposition({ key: "k", at: 0, failed: false }, "k", 10 * 60_000), true);
  assert.equal(reuseComposition({ key: "k", at: 0, failed: false }, "k2", 1), false);
});

test("reuseComposition: a failure is retried after its TTL even with the same key", () => {
  assert.equal(reuseComposition({ key: "k", at: 0, failed: true }, "k", COMPOSE_ERROR_TTL_MS - 1), true);
  assert.equal(reuseComposition({ key: "k", at: 0, failed: true }, "k", COMPOSE_ERROR_TTL_MS), false);
});
