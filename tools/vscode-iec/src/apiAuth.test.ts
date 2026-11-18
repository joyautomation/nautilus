import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { tokenHeaders, tokenPlan, withToken } from "./apiAuth";

// #819: a controller with a token answers a name it does not trust only
// when the request carries the token, reads included; and the token must
// not go, unasked, to a controller URL a workspace chose.

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

const DEFAULT = "http://localhost:8080";
const userToken = { globalValue: "u-tok" };

test("the user's token goes to the default URL and to the user's own URL", () => {
  assert.deepEqual(tokenPlan("u-tok", userToken, DEFAULT, { defaultValue: DEFAULT }, []), { token: "u-tok" });
  const mine = "http://plc.lan:8080";
  assert.deepEqual(tokenPlan("u-tok", userToken, mine, { defaultValue: DEFAULT, globalValue: mine }, []), { token: "u-tok" });
});

test("the user's token does not go to a URL a workspace chose until the user approves its origin", () => {
  const theirs = "http://collector.example:9000";
  const scopes = { defaultValue: DEFAULT, workspaceValue: theirs };
  assert.deepEqual(tokenPlan("u-tok", userToken, theirs, scopes, []), { token: "", ask: "http://collector.example:9000" });
  assert.deepEqual(tokenPlan("u-tok", userToken, theirs, scopes, ["http://other.example:9000"]).token, "");
  assert.deepEqual(tokenPlan("u-tok", userToken, theirs, scopes, ["http://collector.example:9000"]), { token: "u-tok" });
  // A folder setting in a multi-root workspace is the workspace's too.
  const folder = { defaultValue: DEFAULT, globalValue: DEFAULT, workspaceFolderValue: theirs };
  assert.equal(tokenPlan("u-tok", userToken, theirs, folder, []).token, "");
});

test("a workspace's own token goes with the workspace's URL", () => {
  const theirs = "http://plc.site:8080";
  const tok = { globalValue: "u-tok", workspaceValue: "w-tok" };
  assert.deepEqual(tokenPlan("w-tok", tok, theirs, { defaultValue: DEFAULT, workspaceValue: theirs }, []), { token: "w-tok" });
});

test("in an untrusted workspace (its settings ignored) the user's token goes to the user's URL", () => {
  // VS Code ignores the restricted workspace values: the values in effect
  // are the user's, while inspect() still reports what the workspace set.
  const tok = { globalValue: "u-tok", workspaceValue: "w-tok" };
  const url = { defaultValue: DEFAULT, workspaceValue: "http://evil.example" };
  assert.deepEqual(tokenPlan("u-tok", tok, DEFAULT, url, []), { token: "u-tok" });
});

test("the token goes only to the configured controller's origin", () => {
  const mine = "http://plc.lan:8080";
  const scopes = { defaultValue: DEFAULT, globalValue: mine };
  assert.equal(tokenPlan("u-tok", userToken, mine, scopes, [], mine + "/api/stream").token, "u-tok");
  assert.equal(tokenPlan("u-tok", userToken, mine, scopes, [], "http://plc.lan:9100/history").token, "");
  assert.equal(tokenPlan("u-tok", userToken, mine, scopes, [], "https://plc.lan:8080/api/state").token, "");
  assert.equal(tokenPlan("u-tok", userToken, mine, scopes, [], "http://evil.example/api/state").token, "");
});

test("no token, nothing to send or ask", () => {
  assert.deepEqual(tokenPlan("", {}, "http://evil.example", { workspaceValue: "http://evil.example" }, []), { token: "" });
});

test("an untrusted workspace's runtimeUrl and token are restricted settings", () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "package.json"), "utf8"));
  const restricted: string[] = pkg.capabilities?.untrustedWorkspaces?.restrictedConfigurations ?? [];
  for (const key of ["nautilus.runtimeUrl", "nautilus.token"]) {
    assert.ok(restricted.includes(key), `${key} is not in capabilities.untrustedWorkspaces.restrictedConfigurations`);
  }
});

// The risks are a call site that forgets the token, and one that reads the
// token around the rule: every request to the controller goes through
// apiFetch, the live stream sends apiStreamHeaders, and only apiFetch.ts
// reads the nautilus.token setting.
function sources(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return e.name === "testdata" ? [] : sources(p);
    return e.name.endsWith(".ts") && !e.name.endsWith(".test.ts") ? [p] : [];
  });
}

test("no extension source reaches the controller or reads the token around apiFetch", () => {
  const src = path.join(__dirname, "..", "src");
  const bare: string[] = [];
  const tokenReads: string[] = [];
  for (const file of sources(src)) {
    const rel = path.relative(src, file);
    const lines = fs.readFileSync(file, "utf8").split("\n");
    lines.forEach((l, i) => {
      if (/^\s*(\*|\/\/|\/\*)/.test(l)) return; // a comment line
      const code = l.replace(/\/\/.*$/, "");
      // A call of the global fetch; web/engine.ts's own `async fetch(`
      // method definition and `.fetch(` calls are not.
      if (rel !== "apiFetch.ts" && /(?<![\w.])fetch\(/.test(code) && !/\basync fetch\(/.test(code)) bare.push(`${rel}:${i + 1}: ${l.trim()}`);
      if (rel !== "apiFetch.ts" && /get<string>\("token"/.test(code)) tokenReads.push(`${rel}:${i + 1}: ${l.trim()}`);
    });
  }
  assert.deepEqual(bare, [], "call apiFetch (apiFetch.ts), which sends nautilus.token by its rule");
  assert.deepEqual(tokenReads, [], "read the token with apiToken() (apiFetch.ts), which applies the rule");
  const live = fs.readFileSync(path.join(src, "liveValues.ts"), "utf8");
  assert.match(live, /mod\.get\(url, \{ headers: apiStreamHeaders\(url\.toString\(\)\) \}/, "the /api/stream subscription sends the token");
});
