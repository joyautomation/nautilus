// Plain-Node tests for the force-table helpers (no vscode dependency).
// Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import {
  clearForcesConfirmMessage,
  controllerWrite,
  forceApi,
  forceConfirmMessage,
  forcedAddress,
  forcedDescription,
  forcedPillText,
  forceStatusText,
  lowerForces,
  parseForces,
  transitionId,
} from "./forces";

test("parseForces: absent block means nothing forced", () => {
  assert.equal(parseForces({}).size, 0);
  assert.equal(parseForces(undefined).size, 0);
  const f = parseForces({ forces: { StartPB: true, "P101.Speed": 55 } });
  assert.deepEqual([...f], [["StartPB", true], ["P101.Speed", 55]]);
  assert.deepEqual(lowerForces(f), { startpb: true, "p101.speed": 55 });
});

test("forcedAddress: exact, member under a whole-tag force, struct over a member force", () => {
  const f = new Map<string, unknown>([["StartPB", true], ["P101.Speed", 55], ["Tbl", [1, 2]]]);
  assert.equal(forcedAddress(f, "startpb"), "StartPB");
  assert.equal(forcedAddress(f, "P101.Speed"), "P101.Speed");
  assert.equal(forcedAddress(f, "P101"), "P101.Speed"); // the struct is partly forced
  assert.equal(forcedAddress(f, "P101.Run"), undefined);
  assert.equal(forcedAddress(f, "Tbl[2]"), "Tbl");
  assert.equal(forcedAddress(f, "StartPBX"), undefined);
  assert.equal(forcedAddress(new Map(), "StartPB"), undefined);
  // The webview form: lowercased keys in a plain object.
  assert.equal(forcedAddress({ "p101.speed": 55 }, "P101.Speed"), "p101.speed");
});

test("the F badge and the status text", () => {
  assert.equal(forcedPillText("TRUE"), "F TRUE");
  assert.equal(forcedDescription("55"), "F 55");
  assert.equal(forcedDescription("55", "20"), "F 55 (actual 20)");
  assert.equal(forceStatusText(0), "");
  assert.equal(forceStatusText(1), "$(lock) 1 force active");
  assert.equal(forceStatusText(3), "$(lock) 3 forces active");
});

test("transitionId mirrors the transpiler: name, else t<line>", () => {
  assert.equal(transitionId({ name: "Start", line: 9 }), "Start");
  assert.equal(transitionId({ line: 13 }), "t13");
  assert.equal(transitionId({ name: "", line: 4 }), "t4");
});

test("confirm messages name the target controller and the value", () => {
  const m = forceConfirmMessage("http://plc:8080", "StartPB", "TRUE", "FALSE");
  assert.match(m, /Force StartPB \(now FALSE\) to TRUE on http:\/\/plc:8080/);
  assert.match(m, /until it is removed/);
  assert.match(clearForcesConfirmMessage("http://plc:8080", 2), /Remove all 2 forces on http:\/\/plc:8080/);
});

test("controllerWrite: token header, JSON body, refusal surfaced", async () => {
  const seen: { url: string; init: RequestInit }[] = [];
  const fake = (async (url: string, init: RequestInit) => {
    seen.push({ url, init });
    return url.endsWith("/clear")
      ? new Response('{"removed":1}', { status: 200 })
      : new Response("tag X is forced", { status: 409, statusText: "Conflict" });
  }) as unknown as typeof fetch;
  const req = forceApi.force("StartPB", true);
  const r1 = await controllerWrite("http://plc", "s3cret", req.method, req.path, req.body, fake);
  assert.deepEqual(r1, { ok: false, status: 409, message: "tag X is forced" });
  assert.equal(seen[0].url, "http://plc/api/forces");
  assert.equal((seen[0].init.headers as Record<string, string>)["Authorization"], "Bearer s3cret");
  assert.equal(seen[0].init.body, '{"name":"StartPB","value":true}');
  const c = forceApi.clear();
  const r2 = await controllerWrite("http://plc", "", c.method, c.path, undefined, fake);
  assert.equal(r2.ok, true);
  assert.equal((seen[1].init.headers as Record<string, string>)["Authorization"], undefined);
  assert.deepEqual(forceApi.unforce("P101.Speed"), { method: "DELETE", path: "/api/forces/P101.Speed" });
  assert.deepEqual(forceApi.setStep("Fill", "Batch").body, { step: "Fill", pou: "Batch" });
  assert.deepEqual(forceApi.fireTransition("t13").body, { transition: "t13" });
});
