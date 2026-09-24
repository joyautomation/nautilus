// Plain-Node tests for CLI resolution (no vscode dependency).
// Run via `npm test` (compiles then executes with node:test).

import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { isExecutableFile, resolveCli, ResolveEnv } from "./cliResolve";

function env(files: string[], vars: Record<string, string>, platform: NodeJS.Platform = "linux"): ResolveEnv {
  const home = platform === "win32" ? "C:\\Users\\dev" : "/home/dev";
  return { env: vars, platform, home, isExecutable: (p) => files.includes(p) };
}

test("resolveCli: PATH wins when the binary is on it", () => {
  const r = resolveCli("naut", env(["/usr/bin/naut", "/home/dev/go/bin/naut"], { PATH: "/usr/bin:/bin" }));
  assert.deepEqual([r.command, r.found], ["/usr/bin/naut", true]);
});

test("resolveCli: go install's ~/go/bin is found when a desktop launch left it off PATH", () => {
  const r = resolveCli("naut", env(["/home/dev/go/bin/naut"], { PATH: "/usr/bin:/bin" }));
  assert.deepEqual([r.command, r.found], ["/home/dev/go/bin/naut", true]);
});

test("resolveCli: $GOBIN and $GOPATH/bin come before ~/go/bin", () => {
  const files = ["/opt/gobin/naut", "/work/gp/bin/naut", "/home/dev/go/bin/naut"];
  assert.equal(resolveCli("naut", env(files, { GOBIN: "/opt/gobin" })).command, "/opt/gobin/naut");
  assert.equal(resolveCli("naut", env(files, { GOPATH: "/work/gp" })).command, "/work/gp/bin/naut");
});

test("resolveCli: an empty setting means the default name", () => {
  const r = resolveCli("  ", env(["/usr/local/bin/naut"], {}));
  assert.equal(r.command, "/usr/local/bin/naut");
});

test("resolveCli: GNOME's file manager on PATH is never taken for the CLI", () => {
  const r = resolveCli(undefined, env(["/usr/bin/nautilus", "/home/dev/go/bin/naut"], { PATH: "/usr/bin:/bin" }));
  assert.deepEqual([r.command, r.found], ["/home/dev/go/bin/naut", true]);
});

test("resolveCli: an explicit path is taken as given, ~ expanded", () => {
  assert.deepEqual(resolveCli("/nope/naut", env([], {})), { command: "/nope/naut", found: true, searched: [] });
  assert.equal(resolveCli("~/bin/naut", env([], {})).command, "/home/dev/bin/naut");
});

test("resolveCli: not found reports the bare name and every place it looked", () => {
  const r = resolveCli("naut", env([], { PATH: "/usr/bin" }));
  assert.equal(r.found, false);
  assert.equal(r.command, "naut");
  assert.ok(r.searched.includes("/usr/bin"));
  assert.ok(r.searched.includes("/home/dev/go/bin"));
});

test("resolveCli: Windows finds the README install under %LOCALAPPDATA%", () => {
  const r = resolveCli(
    "naut",
    env(["C:\\Users\\dev\\AppData\\Local\\nautilus\\naut.exe"], { Path: "C:\\Windows", LOCALAPPDATA: "C:\\Users\\dev\\AppData\\Local" }, "win32")
  );
  assert.deepEqual([r.command, r.found], ["C:\\Users\\dev\\AppData\\Local\\nautilus\\naut.exe", true]);
});

// ── against the real filesystem of whichever OS runs the suite ───────────────

function sandbox(): { home: string; bin: string; cleanup(): void } {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "nautilus-cli-"));
  const bin = path.join(home, "go", "bin");
  fs.mkdirSync(bin, { recursive: true });
  return { home, bin, cleanup: () => fs.rmSync(home, { recursive: true, force: true }) };
}

function realEnv(home: string, vars: Record<string, string>): ResolveEnv {
  return { env: vars, platform: process.platform, home, isExecutable: isExecutableFile };
}

const exe = process.platform === "win32" ? "naut.exe" : "naut";

test("real fs: a go-installed binary in ~/go/bin is found with nothing on PATH", () => {
  const s = sandbox();
  try {
    const full = path.join(s.bin, exe);
    fs.writeFileSync(full, "", { mode: 0o755 });
    const r = resolveCli("naut", realEnv(s.home, { PATH: path.join(s.home, "empty") }));
    assert.deepEqual([r.command, r.found], [full, true]);
  } finally {
    s.cleanup();
  }
});

test("real fs: a directory named naut is not the CLI", () => {
  const s = sandbox();
  try {
    fs.mkdirSync(path.join(s.bin, exe));
    const r = resolveCli("naut", realEnv(s.home, { PATH: path.join(s.home, "empty") }));
    assert.equal(r.found, false);
  } finally {
    s.cleanup();
  }
});

test("real fs: a file without the execute bit is skipped (POSIX)", { skip: process.platform === "win32" }, () => {
  const s = sandbox();
  try {
    fs.writeFileSync(path.join(s.bin, "naut"), "", { mode: 0o644 });
    assert.equal(isExecutableFile(path.join(s.bin, "naut")), false);
    const r = resolveCli("naut", realEnv(s.home, { PATH: path.join(s.home, "empty") }));
    assert.equal(r.found, false);
  } finally {
    s.cleanup();
  }
});

test("resolveCli: the managed install comes after PATH and before the well-known dirs", () => {
  const managedDir = "/home/dev/.config/Code/User/globalStorage/joyauto.vscode-iec/bin";
  const files = ["/usr/bin/naut", `${managedDir}/naut`, "/home/dev/go/bin/naut"];
  const withPath = resolveCli("naut", { ...env(files, { PATH: "/usr/bin" }), managedDir });
  assert.equal(withPath.command, "/usr/bin/naut", "a developer's own naut on PATH wins");
  const noPath = resolveCli("naut", { ...env(files, { PATH: "/bin" }), managedDir });
  assert.equal(noPath.command, `${managedDir}/naut`, "the managed copy beats ~/go/bin");
  assert.deepEqual(noPath.searched, ["/bin", managedDir]);
  const explicit = resolveCli("/opt/naut", { ...env(files, {}), managedDir });
  assert.equal(explicit.command, "/opt/naut", "an explicit cliPath beats everything");
});
