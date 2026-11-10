import { test } from "node:test";
import assert from "node:assert/strict";
import { agentsOffer, parseAgentsCheck, rememberKey, silenced } from "./agentsMdLogic";

test("parseAgentsCheck: the JSON line naut agents -check -json prints", () => {
  assert.deepEqual(parseAgentsCheck('{"agentsMd":"stale","claudeMd":"current","version":"0.21.0"}\n'), {
    agentsMd: "stale",
    claudeMd: "current",
    version: "0.21.0",
  });
});

test("parseAgentsCheck: an old naut (usage text, an error) or junk means the feature is absent", () => {
  assert.equal(parseAgentsCheck('nautilus: unknown command "agents"\n\nUsage: …'), undefined);
  assert.equal(parseAgentsCheck(""), undefined);
  assert.equal(parseAgentsCheck("{not json"), undefined);
  assert.equal(parseAgentsCheck('{"agentsMd":"sideways","claudeMd":"current"}'), undefined);
  assert.equal(parseAgentsCheck('{"agentsMd":"current"}'), undefined);
});

const c = (agentsMd: string, claudeMd = "current", version = "0.21.0") =>
  parseAgentsCheck(JSON.stringify({ agentsMd, claudeMd, version }))!;

test("agentsOffer: missing adds, stale updates, somebody's own file is appended to, never replaced", () => {
  assert.deepEqual(agentsOffer(c("missing"), "sawmill")?.args, ["agents"]);
  assert.match(agentsOffer(c("missing"), "sawmill")!.message, /^nautilus: sawmill has no AGENTS\.md/);
  assert.deepEqual(agentsOffer(c("stale"), "sawmill")?.args, ["agents"]);
  assert.deepEqual(agentsOffer(c("unmanaged"), "sawmill")?.args, ["agents", "-append"]);
  for (const s of ["missing", "stale", "unmanaged"]) {
    assert.ok(!agentsOffer(c(s), "x")!.args.includes("-force"), `${s} must never offer -force`);
  }
});

test("agentsOffer: a current AGENTS.md needs nothing unless CLAUDE.md lacks the import", () => {
  assert.equal(agentsOffer(c("current"), "x"), undefined);
  assert.equal(agentsOffer(c("current", "missing"), "x"), undefined, "no CLAUDE.md is a choice, not a fault");
  assert.equal(agentsOffer(c("current", "noimport"), "x")?.key, "noimport");
});

test("silenced: Never silences everything; Not now silences only the same offer", () => {
  const stale21 = agentsOffer(c("stale", "current", "0.21.0"), "x")!;
  const stale22 = agentsOffer(c("stale", "current", "0.22.0"), "x")!;
  assert.equal(silenced(undefined, stale21), false);
  assert.equal(silenced("never", stale21), true);
  assert.equal(silenced(`notnow:${stale21.key}`, stale21), true);
  assert.equal(silenced(`notnow:${stale21.key}`, stale22), false, "a newer naut's guidance asks again");
  assert.notEqual(rememberKey("/a"), rememberKey("/b"));
});
