// Plain-Node tests for CLI resolution (no vscode dependency).
// Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { resolveCli, ResolveEnv } from "./cliResolve";

function env(files: string[], vars: Record<string, string>, platform: NodeJS.Platform = "linux"): ResolveEnv {
  const home = platform === "win32" ? "C:\\Users\\dev" : "/home/dev";
  return { env: vars, platform, home, isExecutable: (p) => files.includes(p) };
}

test("resolveCli: PATH wins when the binary is on it", () => {
  const r = resolveCli("nautilus", env(["/usr/bin/nautilus", "/home/dev/go/bin/nautilus"], { PATH: "/usr/bin:/bin" }));
  assert.deepEqual([r.command, r.found], ["/usr/bin/nautilus", true]);
});

test("resolveCli: go install's ~/go/bin is found when a desktop launch left it off PATH", () => {
  const r = resolveCli("nautilus", env(["/home/dev/go/bin/nautilus"], { PATH: "/usr/bin:/bin" }));
  assert.deepEqual([r.command, r.found], ["/home/dev/go/bin/nautilus", true]);
});

test("resolveCli: $GOBIN and $GOPATH/bin come before ~/go/bin", () => {
  const files = ["/opt/gobin/nautilus", "/work/gp/bin/nautilus", "/home/dev/go/bin/nautilus"];
  assert.equal(resolveCli("nautilus", env(files, { GOBIN: "/opt/gobin" })).command, "/opt/gobin/nautilus");
  assert.equal(resolveCli("nautilus", env(files, { GOPATH: "/work/gp" })).command, "/work/gp/bin/nautilus");
});

test("resolveCli: an empty setting means the default name", () => {
  const r = resolveCli("  ", env(["/usr/local/bin/nautilus"], {}));
  assert.equal(r.command, "/usr/local/bin/nautilus");
});

test("resolveCli: an explicit path is taken as given, ~ expanded", () => {
  assert.deepEqual(resolveCli("/nope/nautilus", env([], {})), { command: "/nope/nautilus", found: true, searched: [] });
  assert.equal(resolveCli("~/bin/nautilus", env([], {})).command, "/home/dev/bin/nautilus");
});

test("resolveCli: not found reports the bare name and every place it looked", () => {
  const r = resolveCli("nautilus", env([], { PATH: "/usr/bin" }));
  assert.equal(r.found, false);
  assert.equal(r.command, "nautilus");
  assert.ok(r.searched.includes("/usr/bin"));
  assert.ok(r.searched.includes("/home/dev/go/bin"));
});

test("resolveCli: Windows finds the README install under %LOCALAPPDATA%", () => {
  const r = resolveCli(
    "nautilus",
    env(["C:\\Users\\dev\\AppData\\Local\\nautilus\\nautilus.exe"], { Path: "C:\\Windows", LOCALAPPDATA: "C:\\Users\\dev\\AppData\\Local" }, "win32")
  );
  assert.deepEqual([r.command, r.found], ["C:\\Users\\dev\\AppData\\Local\\nautilus\\nautilus.exe", true]);
});
