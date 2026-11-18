import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { tokenHeaders, withToken } from "./apiAuth";

// #819: a controller with a token answers a name it does not trust only
// when the request carries the token, reads included.

test("withToken adds the bearer token and keeps the caller's headers and options", () => {
  const ctl = new AbortController();
  const init = withToken({ method: "POST", headers: { "Content-Type": "application/json" }, signal: ctl.signal }, "s3cret");
  const h = new Headers(init.headers);
  assert.equal(h.get("authorization"), "Bearer s3cret");
  assert.equal(h.get("content-type"), "application/json");
  assert.equal(init.method, "POST");
  assert.equal(init.signal, ctl.signal);
});

test("withToken with no token adds nothing", () => {
  assert.equal(new Headers(withToken(undefined, "").headers).get("authorization"), null);
  assert.deepEqual(tokenHeaders(""), {});
  assert.deepEqual(tokenHeaders("t"), { Authorization: "Bearer t" });
});

// The risk is a call site that forgets the token: every request the
// extension makes to the controller goes through apiFetch, and the live
// stream subscription sends the token header.
test("no extension source calls fetch() to the controller without the token", () => {
  const src = path.join(__dirname, "..", "src");
  const bare: string[] = [];
  for (const f of fs.readdirSync(src)) {
    if (!f.endsWith(".ts") || f.endsWith(".test.ts") || f === "apiFetch.ts") continue;
    const lines = fs.readFileSync(path.join(src, f), "utf8").split("\n");
    lines.forEach((l, i) => {
      const code = l.replace(/\/\/.*$/, "");
      if (/^\s*\*/.test(l)) return; // a doc-comment line
      if (/(?<![\w.])fetch\(/.test(code)) bare.push(`${f}:${i + 1}: ${l.trim()}`);
    });
  }
  assert.deepEqual(bare, [], "call apiFetch (apiFetch.ts), which sends nautilus.token");
  const live = fs.readFileSync(path.join(src, "liveValues.ts"), "utf8");
  assert.match(live, /mod\.get\(url, \{ headers: apiStreamHeaders\(\) \}/, "the /api/stream subscription sends the token");
});
