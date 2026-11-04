// Plain-Node tests for the PATH setup (temp dirs stand in for $HOME; the
// PowerShell call is a fake registry).
import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { applyUnix, applyWindows, describeResult, onPath, planPathSetup, PROFILE_MARK, withDirOnUserPath } from "./cliPath";

const unixOnly = { skip: process.platform === "win32" };

function home(): { home: string; managed: string } {
  const h = fs.mkdtempSync(path.join(os.tmpdir(), "naut-path-"));
  const managed = path.join(h, "storage", "bin");
  fs.mkdirSync(managed, { recursive: true });
  fs.writeFileSync(path.join(managed, "naut"), "#!/bin/sh\n", { mode: 0o755 });
  return { home: h, managed };
}

test("onPath: trailing slashes, case on Windows only", () => {
  assert.ok(onPath("/a/b/", "/x:/a/b", "linux"));
  assert.ok(!onPath("/A/b", "/a/b", "linux"));
  assert.ok(onPath("C:\\Users\\Me\\bin", "c:\\users\\me\\bin\\;C:\\x", "win32"));
  assert.ok(!onPath("/a", undefined, "linux"));
});

test("plan: per platform", () => {
  const lin = planPathSetup({ platform: "linux", home: "/home/u", env: { PATH: "/usr/bin" }, managedBinDir: "/s/bin" });
  assert.equal(lin.kind, "symlink");
  if (lin.kind === "symlink") {
    assert.equal(lin.link, "/home/u/.local/bin/naut");
    assert.equal(lin.linkDirOnPath, false);
    assert.deepEqual(lin.profiles, ["/home/u/.profile", "/home/u/.bashrc"]);
  }
  const mac = planPathSetup({ platform: "darwin", home: "/Users/u", env: { PATH: "/Users/u/.local/bin" }, managedBinDir: "/s/bin" });
  assert.equal(mac.kind, "symlink");
  if (mac.kind === "symlink") {
    assert.equal(mac.linkDirOnPath, true);
    assert.equal(mac.profiles[0], "/Users/u/.zprofile");
  }
  const win = planPathSetup({ platform: "win32", home: "C:\\Users\\u", env: {}, managedBinDir: "C:\\s\\bin" });
  assert.deepEqual(win, { kind: "userPath", dir: "C:\\s\\bin" });
});

test("unix (mac layout): links, edits the profile once, reports undo", unixOnly, () => {
  const { home: h, managed } = home();
  const plan = planPathSetup({ platform: "darwin", home: h, env: { PATH: "/usr/bin" }, managedBinDir: managed });
  if (plan.kind !== "symlink") throw new Error("plan");
  const r = applyUnix(plan, managed);
  assert.equal(fs.readlinkSync(plan.link), path.join(managed, "naut"));
  const prof = fs.readFileSync(path.join(h, ".zprofile"), "utf8");
  assert.ok(prof.includes(PROFILE_MARK) && prof.includes(".local/bin"));
  assert.ok(!fs.existsSync(path.join(h, ".bash_profile")), "does not create secondary profiles");
  assert.ok(r.undo.length >= 2);
  applyUnix(plan, managed);
  assert.equal(fs.readFileSync(path.join(h, ".zprofile"), "utf8"), prof, "idempotent");
  assert.match(describeResult(r), /new terminal/);
});

test("unix (linux layout): appends to existing profiles; no edit when already on PATH", unixOnly, () => {
  const { home: h, managed } = home();
  fs.writeFileSync(path.join(h, ".profile"), "echo hi");
  fs.writeFileSync(path.join(h, ".bashrc"), "# rc\n");
  const plan = planPathSetup({ platform: "linux", home: h, env: { PATH: "" }, managedBinDir: managed });
  if (plan.kind !== "symlink") throw new Error("plan");
  applyUnix(plan, managed);
  assert.match(fs.readFileSync(path.join(h, ".profile"), "utf8"), /^echo hi\nexport PATH=/);
  assert.match(fs.readFileSync(path.join(h, ".bashrc"), "utf8"), /^# rc\nexport PATH=/);

  const b = home();
  const plan2 = planPathSetup({ platform: "linux", home: b.home, env: { PATH: path.join(b.home, ".local", "bin") }, managedBinDir: b.managed });
  if (plan2.kind !== "symlink") throw new Error("plan");
  applyUnix(plan2, b.managed);
  assert.ok(!fs.existsSync(path.join(b.home, ".profile")));
});

test("unix: leaves a foreign naut alone; refreshes a stale link of ours", unixOnly, () => {
  const { home: h, managed } = home();
  const plan = planPathSetup({ platform: "linux", home: h, env: { PATH: "" }, managedBinDir: managed });
  if (plan.kind !== "symlink") throw new Error("plan");
  fs.mkdirSync(plan.linkDir, { recursive: true });
  fs.writeFileSync(plan.link, "mine");
  const r = applyUnix(plan, managed);
  assert.match(r.skipped ?? "", /left alone/);
  assert.equal(fs.readFileSync(plan.link, "utf8"), "mine");
  assert.equal(r.done.length, 0);

  fs.rmSync(plan.link);
  fs.symlinkSync(path.join(managed, "old-naut"), plan.link);
  applyUnix(plan, managed);
  assert.equal(fs.readlinkSync(plan.link), path.join(managed, "naut"));
});

test("windows: appends once to the user PATH, keeps %VARS%, no rewrite when present", async () => {
  assert.equal(withDirOnUserPath("C:\\a;C:\\b", "C:\\c"), "C:\\a;C:\\b;C:\\c");
  assert.equal(withDirOnUserPath("", "C:\\c"), "C:\\c");
  assert.equal(withDirOnUserPath("C:\\a;c:\\C\\", "C:\\c"), undefined);

  let reg = "%USERPROFILE%\\bin;C:\\a;";
  const writes: string[] = [];
  const run = async (script: string, env: Record<string, string>) => {
    if (script.includes("SetEnvironmentVariable")) {
      reg = env.NAUT_NEW_PATH;
      writes.push(reg);
      return "";
    }
    return `${reg}\r\n`;
  };
  const plan = planPathSetup({ platform: "win32", home: "C:\\Users\\u", env: {}, managedBinDir: "C:\\s\\bin" });
  if (plan.kind !== "userPath") throw new Error("plan");
  const r = await applyWindows(plan, run);
  assert.equal(reg, "%USERPROFILE%\\bin;C:\\a;C:\\s\\bin");
  assert.match(r.undo[0], /remove C:\\s\\bin/);
  await applyWindows(plan, run);
  assert.equal(writes.length, 1);
});
