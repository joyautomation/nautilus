import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { ConfigView, planFromConfig, SettingScopes, tokenHeaders, tokenPlan, withToken } from "./apiAuth";

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

// The binding apiFetch uses: the settings in effect and where they came
// from, as VS Code reports them.
function fakeConfig(scopes: Record<string, SettingScopes>): ConfigView {
  const effective = (s: SettingScopes | undefined) => s?.workspaceFolderValue ?? s?.workspaceValue ?? s?.globalValue ?? s?.defaultValue;
  return {
    get: (key, def) => effective(scopes[key]) ?? def,
    inspect: (key) => scopes[key],
  };
}

test("planFromConfig: a repository's runtimeUrl does not get the user's token unasked", () => {
  const cfg = fakeConfig({
    token: { defaultValue: "", globalValue: "u-tok" },
    runtimeUrl: { defaultValue: DEFAULT, workspaceValue: "http://collector.example:9000" },
  });
  assert.deepEqual(planFromConfig(cfg, [], "http://collector.example:9000/api/stream"), { token: "", ask: "http://collector.example:9000" });
  assert.deepEqual(planFromConfig(cfg, ["http://collector.example:9000"], "http://collector.example:9000/api/stream"), { token: "u-tok" });
  const own = fakeConfig({ token: { defaultValue: "", globalValue: "u-tok" }, runtimeUrl: { defaultValue: DEFAULT, globalValue: "http://plc.lan:8080" } });
  assert.deepEqual(planFromConfig(own, [], "http://plc.lan:8080/api/state"), { token: "u-tok" });
  assert.deepEqual(planFromConfig(own, [], "http://other.lan:8080/api/state"), { token: "" });
});

test("planFromConfig: in a remote window a user-scope runtimeUrl needs approval (devcontainer.json lands there)", () => {
  // Dev Containers merges devcontainer.json customizations.vscode.settings
  // into the remote user settings, so globalValue may be the repository's.
  const cfg = fakeConfig({
    token: { defaultValue: "", globalValue: "u-tok" },
    runtimeUrl: { defaultValue: DEFAULT, globalValue: "http://collector.example:9000" },
  });
  const target = "http://collector.example:9000/api/stream";
  assert.deepEqual(planFromConfig(cfg, [], target, true), { token: "", ask: "http://collector.example:9000" });
  assert.deepEqual(planFromConfig(cfg, ["http://collector.example:9000"], target, true), { token: "u-tok" });
  assert.deepEqual(planFromConfig(cfg, [], target, false), { token: "u-tok" }, "a local window's user settings are the user's");
  // The default URL is still the user's own, remote or not.
  const def = fakeConfig({ token: { defaultValue: "", globalValue: "u-tok" }, runtimeUrl: { defaultValue: DEFAULT } });
  assert.deepEqual(planFromConfig(def, [], DEFAULT + "/api/state", true), { token: "u-tok" });
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

test("no extension source reaches the controller or reads the token around the rule", () => {
  const src = path.join(__dirname, "..", "src");
  const bare: string[] = [];
  const tokenReads: string[] = [];
  const authHeaders: string[] = [];
  for (const file of sources(src)) {
    const rel = path.relative(src, file);
    const lines = fs.readFileSync(file, "utf8").split("\n");
    lines.forEach((l, i) => {
      if (/^\s*(\*|\/\/|\/\*)/.test(l)) return; // a comment line
      const code = l.replace(/\/\/.*$/, "");
      const at = `${rel}:${i + 1}: ${l.trim()}`;
      // A call of the global fetch; web/engine.ts's own `async fetch(`
      // method definition and `.fetch(` calls are not.
      if (rel !== "apiFetch.ts" && /(?<![\w.])fetch\(/.test(code) && !/\basync fetch\(/.test(code)) bare.push(at);
      // Only planFromConfig (apiAuth.ts) reads the setting.
      if (rel !== "apiAuth.ts" && /\bget(<string>)?\(\s*"token"/.test(code)) tokenReads.push(at);
      // Only withToken/tokenHeaders and controllerWrite (handed the gated
      // token) build the header.
      if (rel !== "apiAuth.ts" && rel !== "forces.ts" && /\bAuthorization\b/.test(code)) authHeaders.push(at);
    });
  }
  assert.deepEqual(bare, [], "call apiFetch (apiFetch.ts), which sends nautilus.token by its rule");
  assert.deepEqual(tokenReads, [], "read the token with apiToken() (apiFetch.ts → planFromConfig), which applies the rule");
  assert.deepEqual(authHeaders, [], "let apiFetch add the token; a hand-built header bypasses the rule");
  const live = fs.readFileSync(path.join(src, "liveValues.ts"), "utf8");
  assert.match(live, /mod\.get\(url, \{ headers: apiStreamHeaders\(url\.toString\(\)\) \}/, "the /api/stream subscription sends the token");
  assert.match(live, /return apiToken\(this\.runtimeUrl\(\)\);/, "controllerWrite gets the token for the controller's origin");
  const fetchSrc = fs.readFileSync(path.join(src, "apiFetch.ts"), "utf8");
  assert.match(fetchSrc, /planFromConfig\(view, approved\(\), target, vscode\.env\.remoteName !== undefined\)/, "a remote window is passed as remote (devcontainer.json settings land in user scope)");
});
