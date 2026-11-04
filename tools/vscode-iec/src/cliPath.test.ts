// Plain-Node tests for the PATH setup (temp dirs stand in for $HOME; the
// PowerShell call is a fake registry).
import { test } from "node:test";
import * as assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  applyUnix,
  applyWindows,
  danglingOwnLink,
  describeResult,
  isInside,
  onPath,
  planPathSetup,
  PROFILE_MARK,
  removeUnix,
  removeWindows,
  sameFile,
  SymlinkPlan,
  unixChanges,
  withDirOnUserPath,
  withoutDirOnUserPath,
  WRITE_USER_PATH,
} from "./cliPath";

const unixOnly = { skip: process.platform === "win32" };

function home(): { home: string; managed: string } {
  const h = fs.mkdtempSync(path.join(os.tmpdir(), "naut-path-"));
  const managed = path.join(h, "storage", "bin");
  fs.mkdirSync(managed, { recursive: true });
  fs.writeFileSync(path.join(managed, "naut"), "#!/bin/sh\n", { mode: 0o755 });
  return { home: h, managed };
}

function unixPlan(h: string, managed: string, env: Record<string, string | undefined>, platform: NodeJS.Platform = "linux"): SymlinkPlan {
  const plan = planPathSetup({ platform, home: h, env, managedBinDir: managed });
  if (plan.kind !== "symlink") throw new Error("plan");
  return plan;
}

test("onPath: trailing slashes, case on Windows only", () => {
  assert.ok(onPath("/a/b/", "/x:/a/b", "linux"));
  assert.ok(!onPath("/A/b", "/a/b", "linux"));
  assert.ok(onPath("C:\\Users\\Me\\bin", "c:\\users\\me\\bin\\;C:\\x", "win32"));
  assert.ok(!onPath("/a", undefined, "linux"));
});

test("isInside: a sibling sharing a prefix is not inside", () => {
  assert.ok(isInside("/s/bin/naut", "/s/bin", "linux"));
  assert.ok(isInside("/s/bin", "/s/bin/", "linux"));
  assert.ok(!isInside("/s/bin2/naut", "/s/bin", "linux"));
  assert.ok(isInside("c:\\S\\Bin\\naut.exe", "C:\\s\\bin", "win32"));
  assert.ok(!isInside("C:\\s\\bin2\\naut.exe", "C:\\s\\bin", "win32"));
});

test("sameFile follows symlinks (our ~/.local/bin link is the managed binary)", unixOnly, () => {
  const { home: h, managed } = home();
  const link = path.join(h, "link-naut");
  fs.symlinkSync(path.join(managed, "naut"), link);
  assert.ok(sameFile(link, path.join(managed, "naut")));
  assert.ok(!sameFile(path.join(h, "storage"), path.join(managed, "naut")));
  // Missing files fall back to comparing the paths.
  assert.ok(sameFile(path.join(h, "gone", "..", "x"), path.join(h, "x")));
  assert.ok(!sameFile(link, undefined));
});

test("plan: per platform and login shell", () => {
  const lin = unixPlan("/home/u", "/s/bin", { PATH: "/usr/bin" });
  assert.equal(lin.link, "/home/u/.local/bin/naut");
  assert.equal(lin.linkDirOnPath, false);
  assert.deepEqual(lin.profiles, [
    { file: "/home/u/.profile", create: true },
    { file: "/home/u/.bashrc", create: false },
    { file: "/home/u/.zshrc", create: false },
  ]);
  assert.equal(lin.fishFile, undefined);
  const linZsh = unixPlan("/home/u", "/s/bin", { PATH: "/usr/bin", SHELL: "/usr/bin/zsh" });
  assert.deepEqual(linZsh.profiles[0], { file: "/home/u/.zshrc", create: true });
  const linFish = unixPlan("/home/u", "/s/bin", { SHELL: "/usr/bin/fish" });
  assert.equal(linFish.fishFile, "/home/u/.config/fish/conf.d/nautilus.fish");

  const mac = unixPlan("/Users/u", "/s/bin", { PATH: "/Users/u/.local/bin" }, "darwin");
  assert.equal(mac.linkDirOnPath, true);
  assert.deepEqual(mac.profiles[0], { file: "/Users/u/.zprofile", create: true });
  const macBash = unixPlan("/Users/u", "/s/bin", { SHELL: "/bin/bash" }, "darwin");
  assert.deepEqual(macBash.profiles[0], { file: "/Users/u/.bash_profile", create: true });

  const win = planPathSetup({ platform: "win32", home: "C:\\Users\\u", env: {}, managedBinDir: "C:\\s\\bin" });
  assert.deepEqual(win, { kind: "userPath", dir: "C:\\s\\bin" });
});

test("unixChanges lists the changes and makes none", unixOnly, () => {
  const { home: h, managed } = home();
  fs.writeFileSync(path.join(h, ".bashrc"), "# rc\n");
  const plan = unixPlan(h, managed, { PATH: "/usr/bin" });
  const { changes, blocked } = unixChanges(plan, managed);
  assert.equal(blocked, undefined);
  assert.deepEqual(
    changes.map((c) => c.describe),
    [
      `create the link ~/.local/bin/naut → ${path.join(managed, "naut")}`,
      "create ~/.profile (appends ~/.local/bin to PATH)",
      "add one line to ~/.bashrc (appends ~/.local/bin to PATH)",
    ]
  );
  assert.ok(!fs.existsSync(plan.linkDir));
  assert.ok(!fs.existsSync(path.join(h, ".profile")));
  assert.equal(fs.readFileSync(path.join(h, ".bashrc"), "utf8"), "# rc\n");
});

test("unix (mac layout): links, edits the profile once, points at the undo command", unixOnly, () => {
  const { home: h, managed } = home();
  const plan = unixPlan(h, managed, { PATH: "/usr/bin" }, "darwin");
  const r = applyUnix(plan, managed);
  assert.equal(fs.readlinkSync(plan.link), path.join(managed, "naut"));
  const prof = fs.readFileSync(path.join(h, ".zprofile"), "utf8");
  assert.ok(prof.includes(PROFILE_MARK) && prof.includes(".local/bin"));
  assert.ok(!fs.existsSync(path.join(h, ".bash_profile")), "does not create secondary profiles");
  assert.deepEqual(r.undo, ['run "nautilus: Remove naut from PATH"']);
  assert.deepEqual(unixChanges(plan, managed).changes, [], "nothing left to do");
  applyUnix(plan, managed);
  assert.equal(fs.readFileSync(path.join(h, ".zprofile"), "utf8"), prof, "idempotent");
  assert.match(describeResult(r), /new terminal window outside VS Code/);
});

test("unix (linux layout): appends to existing profiles; no edit when already on PATH", unixOnly, () => {
  const { home: h, managed } = home();
  fs.writeFileSync(path.join(h, ".profile"), "echo hi");
  fs.writeFileSync(path.join(h, ".bashrc"), "# rc\n");
  fs.writeFileSync(path.join(h, ".zshrc"), "# z\n");
  const plan = unixPlan(h, managed, { PATH: "" });
  const r = applyUnix(plan, managed);
  assert.match(fs.readFileSync(path.join(h, ".profile"), "utf8"), /^echo hi\ncase ":\$PATH:"/);
  assert.match(fs.readFileSync(path.join(h, ".bashrc"), "utf8"), /^# rc\ncase /);
  assert.match(fs.readFileSync(path.join(h, ".zshrc"), "utf8"), /^# z\ncase /, "zsh on Linux is covered");
  assert.ok(r.done.some((l) => l.includes("~/.profile, ~/.bashrc, ~/.zshrc")), r.done.join("|"));

  const b = home();
  applyUnix(unixPlan(b.home, b.managed, { PATH: path.join(b.home, ".local", "bin") }), b.managed);
  assert.ok(!fs.existsSync(path.join(b.home, ".profile")));
});

test("unix: zsh on Linux gets ~/.zshrc created; fish gets a conf.d file", unixOnly, () => {
  const { home: h, managed } = home();
  fs.mkdirSync(path.join(h, ".config", "fish"), { recursive: true });
  const plan = unixPlan(h, managed, { PATH: "/usr/bin", SHELL: "/usr/bin/zsh" });
  applyUnix(plan, managed);
  assert.match(fs.readFileSync(path.join(h, ".zshrc"), "utf8"), /\.local\/bin/);
  assert.ok(!fs.existsSync(path.join(h, ".profile")), "only the login shell's file is created");
  const fish = fs.readFileSync(path.join(h, ".config", "fish", "conf.d", "nautilus.fish"), "utf8");
  assert.match(fish, /^contains -- \$HOME\/\.local\/bin \$PATH; or set -gx PATH \$PATH \$HOME\/\.local\/bin/);
  assert.ok(fish.includes(PROFILE_MARK));
});

// The line really appends, and only once however often it is sourced (nested shells).
for (const sh of ["sh", "bash", "zsh"]) {
  const which = ["/bin", "/usr/bin"].map((d) => path.join(d, sh)).find((p) => fs.existsSync(p));
  test(`the profile line appends ~/.local/bin once under ${sh}`, { skip: process.platform === "win32" || !which }, () => {
    const { home: h, managed } = home();
    const plan = unixPlan(h, managed, { PATH: "/usr/bin" });
    const rc = path.join(h, "rc");
    fs.writeFileSync(rc, `${plan.line}\n`);
    const run = (p: string) =>
      execFileSync(which!, ["-c", `. "${rc}"; . "${rc}"; printf %s "$PATH"`], { env: { HOME: h, PATH: p }, encoding: "utf8" });
    assert.equal(run("/usr/bin:/bin"), `/usr/bin:/bin:${h}/.local/bin`);
    assert.equal(run(`${h}/.local/bin:/usr/bin:/bin`), `${h}/.local/bin:/usr/bin:/bin`, "already there: unchanged");
  });
}

test("unix: leaves a foreign naut alone; refreshes a stale link of ours; a /bin2 sibling isn't ours", unixOnly, () => {
  const { home: h, managed } = home();
  const plan = unixPlan(h, managed, { PATH: "" });
  fs.mkdirSync(plan.linkDir, { recursive: true });
  fs.writeFileSync(plan.link, "mine");
  const r = applyUnix(plan, managed);
  assert.match(r.skipped ?? "", /left alone/);
  assert.equal(fs.readFileSync(plan.link, "utf8"), "mine");
  assert.equal(r.done.length, 0);
  assert.ok(!fs.existsSync(path.join(h, ".profile")), "no PATH line that would expose the foreign naut");

  fs.rmSync(plan.link);
  fs.symlinkSync(path.join(managed, "old-naut"), plan.link);
  applyUnix(plan, managed);
  assert.equal(fs.readlinkSync(plan.link), path.join(managed, "naut"));

  // Same prefix, different directory: …/bin2/naut is not the extension's.
  const sibling = managed + "2";
  fs.mkdirSync(sibling);
  fs.writeFileSync(path.join(sibling, "naut"), "other", { mode: 0o755 });
  fs.rmSync(plan.link);
  fs.symlinkSync(path.join(sibling, "naut"), plan.link);
  assert.match(unixChanges(plan, managed).blocked ?? "", /not made by this extension/);
  assert.match(applyUnix(plan, managed).skipped ?? "", /left alone/);
  assert.equal(fs.readlinkSync(plan.link), path.join(sibling, "naut"));
  assert.deepEqual(removeUnix({ platform: "linux", home: h, env: {}, managedBinDir: managed }).filter((l) => l.includes("link")), []);
  assert.ok(fs.existsSync(plan.link), "remove leaves someone else's link");
});

test("removeUnix undoes only our link, our lines, our fish file", unixOnly, () => {
  const { home: h, managed } = home();
  fs.writeFileSync(path.join(h, ".bashrc"), "# rc\nalias ll='ls -l'\n");
  fs.mkdirSync(path.join(h, ".config", "fish"), { recursive: true });
  const plan = unixPlan(h, managed, { PATH: "/usr/bin" });
  applyUnix(plan, managed);
  const done = removeUnix({ platform: "linux", home: h, env: {}, managedBinDir: managed });
  assert.ok(!fs.existsSync(plan.link));
  assert.equal(fs.readFileSync(path.join(h, ".bashrc"), "utf8"), "# rc\nalias ll='ls -l'\n");
  assert.ok(!fs.readFileSync(path.join(h, ".profile"), "utf8").includes(PROFILE_MARK));
  assert.ok(!fs.existsSync(path.join(h, ".config", "fish", "conf.d", "nautilus.fish")));
  assert.equal(done.length, 4, done.join("|"));
  assert.deepEqual(removeUnix({ platform: "linux", home: h, env: {}, managedBinDir: managed }), [], "second run: nothing");
});

test("danglingOwnLink: our link whose target is gone", unixOnly, () => {
  const { home: h, managed } = home();
  const plan = unixPlan(h, managed, { PATH: "" });
  applyUnix(plan, managed);
  assert.equal(danglingOwnLink(h, managed), undefined);
  fs.rmSync(path.join(managed, "naut"));
  assert.equal(danglingOwnLink(h, managed), plan.link);
});

/** A fake HKCU\Environment that runs the two PowerShell scripts the way
 * Windows PowerShell 5.1 would, recording the value kind each write uses.
 * Like the real thing it can only hand text back through stdout in the OEM
 * code page: anything outside ASCII comes back as U+FFFD, so only an
 * ASCII-safe encoding (base64) survives. `failWrite` makes SetValue throw
 * (Constrained Language Mode, policy, a value too long): without
 * $ErrorActionPreference = 'Stop' and an explicit exit 1, PowerShell would
 * carry on and exit 0. */
function fakeRegistry(initial: string, o: { failWrite?: boolean } = {}) {
  const reg = { value: initial, kind: "ExpandString", writes: [] as { value: string; kind: string; broadcast: boolean }[] };
  const oem = (s: string) => s.replace(/[^\x00-\x7f]/g, "\ufffd");
  const run = async (script: string, env: Record<string, string>) => {
    if (script.includes("SetValue(")) {
      if (script.includes("SetEnvironmentVariable")) throw new Error("SetEnvironmentVariable writes REG_SZ");
      assert.match(script, /FromBase64String\(\$env:NAUT_NEW_PATH_B64\)/);
      const m = /SetValue\('Path', \$p, \[Microsoft\.Win32\.RegistryValueKind\]::(\w+)\)/.exec(script);
      if (!m) throw new Error(`unexpected write script: ${script}`);
      if (o.failWrite) {
        const stops = /^\$ErrorActionPreference = 'Stop'/.test(script) && /catch \{[^}]*exit 1/.test(script);
        if (!stops) return ""; // the error is printed, the script goes on, exit 0
        throw new Error("couldn't write the user PATH: Requested registry access is not allowed.");
      }
      reg.value = Buffer.from(env.NAUT_NEW_PATH_B64, "base64").toString("utf16le");
      reg.kind = m[1];
      reg.writes.push({ value: reg.value, kind: m[1], broadcast: /SendMessageTimeout\(\[IntPtr\]0xffff, 0x1A, .*'Environment'/.test(script) });
      return "";
    }
    assert.match(script, /GetValue\('Path', '', 'DoNotExpandEnvironmentNames'\)/);
    const out = /ToBase64String\(\[Text\.Encoding\]::Unicode\.GetBytes\(\$v\)\)/.test(script)
      ? Buffer.from(reg.value, "utf16le").toString("base64")
      : reg.value;
    return `${oem(out)}\r\n`;
  };
  return { reg, run };
}

test("windows: a non-ASCII entry survives the read and the write-back", async () => {
  const jose = "C:\\Users\\José\\AppData\\Local\\Programs\\Microsoft VS Code\\bin";
  const { reg, run } = fakeRegistry(`%USERPROFILE%\\bin;${jose};C:\\Ünïcødé 日本`);
  const plan = planPathSetup({ platform: "win32", home: "C:\\Users\\José", env: {}, managedBinDir: "C:\\Users\\José\\s\\bin" });
  if (plan.kind !== "userPath") throw new Error("plan");
  await applyWindows(plan, run);
  assert.equal(reg.value, `%USERPROFILE%\\bin;${jose};C:\\Ünïcødé 日本;C:\\Users\\José\\s\\bin`);
  assert.ok(!reg.value.includes("\ufffd"));
  await removeWindows(plan.dir, run);
  assert.equal(reg.value, `%USERPROFILE%\\bin;${jose};C:\\Ünïcødé 日本`);
});

test("windows: a write that fails rejects (no 'added' claimed) and changes nothing", async () => {
  const { reg, run } = fakeRegistry("C:\\a", { failWrite: true });
  const plan = planPathSetup({ platform: "win32", home: "C:\\Users\\u", env: {}, managedBinDir: "C:\\s\\bin" });
  if (plan.kind !== "userPath") throw new Error("plan");
  await assert.rejects(applyWindows(plan, run), /couldn't write the user PATH/);
  await assert.rejects(removeWindows("C:\\a", run), /couldn't write the user PATH/);
  assert.equal(reg.value, "C:\\a");
  assert.equal(reg.writes.length, 0);
});

test("windows: appends once to the user PATH as REG_EXPAND_SZ, keeps %VARS%, broadcasts", async () => {
  assert.equal(withDirOnUserPath("C:\\a;C:\\b", "C:\\c"), "C:\\a;C:\\b;C:\\c");
  assert.equal(withDirOnUserPath("", "C:\\c"), "C:\\c");
  assert.equal(withDirOnUserPath("C:\\a;c:\\C\\", "C:\\c"), undefined);
  assert.ok(!WRITE_USER_PATH.includes("SetEnvironmentVariable"));

  const { reg, run } = fakeRegistry("%USERPROFILE%\\AppData\\Local\\Microsoft\\WindowsApps;C:\\a;");
  const plan = planPathSetup({ platform: "win32", home: "C:\\Users\\u", env: {}, managedBinDir: "C:\\s\\bin" });
  if (plan.kind !== "userPath") throw new Error("plan");
  const r = await applyWindows(plan, run);
  assert.equal(reg.value, "%USERPROFILE%\\AppData\\Local\\Microsoft\\WindowsApps;C:\\a;C:\\s\\bin");
  assert.deepEqual(reg.writes.map((w) => [w.kind, w.broadcast]), [["ExpandString", true]]);
  assert.deepEqual(r.undo, ['run "nautilus: Remove naut from PATH"']);
  await applyWindows(plan, run);
  assert.equal(reg.writes.length, 1, "no rewrite when present");
});

test("windows: remove drops only our entry, still REG_EXPAND_SZ", async () => {
  assert.equal(withoutDirOnUserPath("C:\\a;c:\\s\\BIN\\;%X%\\y", "C:\\s\\bin"), "C:\\a;%X%\\y");
  assert.equal(withoutDirOnUserPath("C:\\a;C:\\s\\bin2", "C:\\s\\bin"), undefined);
  const { reg, run } = fakeRegistry("%USERPROFILE%\\bin;C:\\s\\bin;C:\\a");
  assert.deepEqual(await removeWindows("C:\\s\\bin", run), ["removed C:\\s\\bin from your user PATH"]);
  assert.equal(reg.value, "%USERPROFILE%\\bin;C:\\a");
  assert.equal(reg.kind, "ExpandString");
  assert.deepEqual(await removeWindows("C:\\s\\bin", run), []);
  assert.equal(reg.writes.length, 1);
});
