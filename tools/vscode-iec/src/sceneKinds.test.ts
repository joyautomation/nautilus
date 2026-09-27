// The 3D palette's list and the kind-component marker — pure logic, run
// with `npm test` (node --test over out/). The built-in list is held to
// hmi-3d's contract when the monorepo is around.

import { strict as assert } from "node:assert";
import { test } from "node:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { BUILTIN_KINDS, entryMembers, isKindComponent, paletteKinds } from "./sceneKinds";

const skid = {
  type: "Skid",
  members: ["Fault"],
  status: "{Fault?FAULT:ok}",
  assembly: {
    nodes: [
      { id: "pump", kind: "pump", tag: "Pump", pos: [0, 0, 0] },
      { id: "valve", kind: "valve", tag: "Valve", pos: [0.4, 0, 0], bind: { cmd: "!Valve.Cmd" } },
    ],
    pipes: [{ points: [[0, 0, 0], [1, 0, 0]], bind: { flowing: "Pump.Running" } }],
  },
};

test("paletteKinds lists the built-ins alone for a malformed or empty document", () => {
  for (const doc of [undefined, null, "x", [], {}, { kinds: 3 }]) {
    const rows = paletteKinds(doc);
    assert.deepEqual(rows.map((r) => r.name), ["tank", "pump", "valve"]);
    assert.ok(rows.every((r) => r.definedBy === "builtin"));
  }
});

test("paletteKinds: a re-modelled built-in stays one row; custom kinds follow, sorted, with how each is defined", () => {
  const rows = paletteKinds({
    kinds: {
      pump: { model: "models/pump.glb", drive: [{ mesh: "Coupling", spin: { axis: "x", revPerS: { bind: "Rpm" } } }] },
      valve: { type: "XV" },
      skid: { type: "Skid", members: ["Fault"], component: "hmi/src/lib/Skid.svelte" },
      "skid-data": skid,
      beacon: { type: "Switch", model: "models/beacon.glb", status: "{PortsUp} up" },
      server: { type: "Server", members: ["Online"] },
      bad: "nope",
    },
    nodes: [],
  });
  assert.deepEqual(rows.map((r) => [r.name, r.definedBy]), [
    ["tank", "builtin"],
    ["pump", "model"],
    ["valve", "builtin"],
    ["bad", "declared"],
    ["beacon", "model"],
    ["server", "declared"],
    ["skid", "component"],
    ["skid-data", "assembly"],
  ]);
  const by = Object.fromEntries(rows.map((r) => [r.name, r]));
  assert.deepEqual(by.pump.members, ["Running", "Fault", "Speed", "Rpm"]);
  assert.equal(by.pump.source, "models/pump.glb");
  assert.equal(by.valve.type, "XV");
  assert.equal(by.skid.source, "hmi/src/lib/Skid.svelte");
  assert.deepEqual(by.skid.members, ["Fault"]);
  assert.deepEqual(by["skid-data"].members, ["Fault", "Pump", "Valve"]);
  assert.equal(by["skid-data"].parts, 2);
  assert.deepEqual(by.beacon.members, ["PortsUp"]);
  assert.equal(by.server.type, "Server");
});

test("entryMembers is the union hmi-3d and naut check compute", () => {
  assert.deepEqual(entryMembers(skid), ["Fault", "Pump", "Valve"]);
  assert.deepEqual(entryMembers({ drive: [{ mesh: "M", tint: { bind: "!Running", on: "running" } }, { mesh: "F", scale: { axis: "y", to: { bind: "Level" } } }] }), ["Running", "Level"]);
  assert.deepEqual(entryMembers(undefined), []);
});

test("isKindComponent recognises a kind's file by its module-script `kind` export", () => {
  assert.equal(isKindComponent(`<script lang="ts" module>\n  export const kind = { type: 'Skid' };\n</script>\n<script lang="ts">let { value } = $props();</script>`), true);
  assert.equal(isKindComponent(`<script context="module">export const kind = {};</script>`), true);
  assert.equal(isKindComponent(`<script module>const kind = {}; export { kind };</script>`), true);
  assert.equal(isKindComponent(`<script module>export function kind() {}</script>`), true);
  // The word in the instance script, a comment, or markup does not count.
  assert.equal(isKindComponent(`<script lang="ts">export const kind = 1;</script>`), false);
  assert.equal(isKindComponent(`<script module>// export const kind\nexport const kinds = [];</script>`), false);
  assert.equal(isKindComponent(`<div>kind</div>`), false);
  assert.equal(isKindComponent(""), false);
});

const HMI3D_KINDS = path.join(__dirname, "..", "..", "..", "hmi-3d", "src", "lib", "kinds.ts");
test(
  "BUILTIN_KINDS mirrors hmi-3d's BUILTIN_CONTRACT",
  { skip: !fs.existsSync(HMI3D_KINDS) ? "hmi-3d not in this checkout" : false },
  () => {
    const src = fs.readFileSync(HMI3D_KINDS, "utf8");
    for (const b of BUILTIN_KINDS) {
      const re = new RegExp(`${b.name}:\\s*\\{\\s*type:\\s*'${b.type}',\\s*members:\\s*\\[([^\\]]*)\\]`);
      const m = src.match(re);
      assert.ok(m, `hmi-3d declares ${b.name} as ${b.type}`);
      assert.deepEqual(m![1].split(",").map((s) => s.trim().replace(/'/g, "")), b.members);
    }
  }
);
