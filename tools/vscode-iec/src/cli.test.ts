// The wiring in cli.ts, run against a stub `vscode` module: which naut counts
// as the extension's own (through the ~/.local/bin link), what the integrated
// terminals' PATH gets, and that nothing outside VS Code changes without the
// user's OK. $HOME and PATH point at temp dirs; the "naut" binaries are tiny
// shell scripts that answer `naut version`.
import { test, beforeEach } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import Module from "node:module";

type Handler = (msg: string, items: string[], opts?: { modal?: boolean; detail?: string }) => string | undefined;

const ui = {
  info: [] as { msg: string; items: string[]; opts?: { modal?: boolean; detail?: string } }[],
  warn: [] as string[],
  onInfo: (() => undefined) as Handler,
  onWarn: (() => undefined) as Handler,
  progress: 0,
  onProgress: (): Promise<unknown> => Promise.reject(new Error("cancelled")),
  cliPath: "",
};

function splitArgs(args: unknown[]): { opts?: { modal?: boolean; detail?: string }; items: string[] } {
  const opts = typeof args[0] === "object" && args[0] !== null ? (args.shift() as { modal?: boolean; detail?: string }) : undefined;
  return { opts, items: args as string[] };
}

const vscodeStub = {
  window: {
    showInformationMessage: (msg: string, ...args: unknown[]) => {
      const { opts, items } = splitArgs(args);
      ui.info.push({ msg, items, opts });
      return Promise.resolve(ui.onInfo(msg, items, opts));
    },
    showWarningMessage: (msg: string, ...args: unknown[]) => {
      ui.warn.push(msg);
      return Promise.resolve(ui.onWarn(msg, splitArgs(args).items));
    },
    showErrorMessage: (msg: string) => {
      ui.warn.push(msg);
      return Promise.resolve(undefined);
    },
    withProgress: () => {
      ui.progress++;
      return ui.onProgress();
    },
    createOutputChannel: () => ({ appendLine: () => undefined, show: () => undefined }),
  },
  commands: { executeCommand: () => Promise.resolve(undefined) },
  workspace: {
    getConfiguration: () => ({ get: () => ui.cliPath, update: () => Promise.resolve() }),
  },
  env: { openExternal: () => Promise.resolve(true), clipboard: { writeText: () => Promise.resolve() } },
  Uri: { parse: (s: string) => s },
  ProgressLocation: { Notification: 15 },
  ConfigurationTarget: { Global: 1 },
};

const mod = Module as unknown as { _load: (req: string, ...rest: unknown[]) => unknown };
const realLoad = mod._load;
mod._load = function (req: string, ...rest: unknown[]) {
  return req === "vscode" ? vscodeStub : realLoad.call(this, req, ...rest);
};
// eslint-disable-next-line @typescript-eslint/no-var-requires
const cli = require("./cli") as typeof import("./cli");

class Memento {
  data = new Map<string, unknown>();
  get<T>(k: string): T | undefined {
    return this.data.get(k) as T | undefined;
  }
  update(k: string, v: unknown): Promise<void> {
    if (v === undefined) this.data.delete(k);
    else this.data.set(k, v);
    return Promise.resolve();
  }
}

class EnvCollection {
  description = "";
  ops: string[] = [];
  prepend(v: string, val: string) {
    this.ops.push(`prepend ${v} ${val}`);
  }
  append(v: string, val: string) {
    this.ops.push(`append ${v} ${val}`);
  }
  replace(v: string, val: string) {
    this.ops.push(`replace ${v} ${val}`);
  }
  clear() {
    this.ops.push("clear");
  }
}

const unixOnly = { skip: process.platform === "win32" };
const savedEnv = { HOME: process.env.HOME, PATH: process.env.PATH, SHELL: process.env.SHELL };
process.on("exit", () => Object.assign(process.env, savedEnv));

function fakeNaut(file: string, version: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, `#!/bin/sh\necho "naut ${version}"\n`, { mode: 0o755 });
}

/** A temp $HOME with the extension's storage; `installed` writes the managed naut. */
function setup(o: { installed?: boolean; version?: string; minVersion?: string; path?: string[] } = {}) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "naut-cli-"));
  const storage = path.join(home, "storage");
  const managedDir = path.join(storage, "bin");
  const managed = path.join(managedDir, "naut");
  if (o.installed ?? true) fakeNaut(managed, o.version ?? "0.1.0");
  const empty = path.join(home, "empty");
  fs.mkdirSync(empty);
  process.env.HOME = home;
  process.env.SHELL = "/bin/bash";
  process.env.PATH = [...(o.path ?? []), empty].join(path.delimiter);
  const state = new Memento();
  const envColl = new EnvCollection();
  const ctx = {
    globalStorageUri: { fsPath: storage },
    extension: { packageJSON: { nautilusCli: { minVersion: o.minVersion ?? "0.0.1" } } },
    globalState: state,
    environmentVariableCollection: envColl,
  };
  const init = () => cli.initCli(ctx as never);
  return { home, managedDir, managed, link: path.join(home, ".local", "bin", "naut"), state, envColl, init };
}

const log = { appendLine: () => undefined } as never;

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
}

beforeEach(() => {
  ui.info = [];
  ui.warn = [];
  ui.onInfo = () => undefined;
  ui.onWarn = () => undefined;
  ui.progress = 0;
  ui.onProgress = () => Promise.reject(new Error("cancelled"));
  ui.cliPath = "";
});

// ── HIGH 1: the managed copy reached through ~/.local/bin is still ours ────

function linkedOnPath() {
  const s = setup({ version: "0.1.0", minVersion: "9.9.9" });
  fs.mkdirSync(path.dirname(s.link), { recursive: true });
  fs.symlinkSync(s.managed, s.link);
  process.env.PATH = `${path.dirname(s.link)}${path.delimiter}${process.env.PATH}`;
  s.init();
  return s;
}

test("the ~/.local/bin link resolves first and counts as the managed copy", unixOnly, () => {
  const s = linkedOnPath();
  assert.equal(cli.resolveCliNow().command, s.link, "PATH (with ~/.local/bin) is searched before the managed dir");
  assert.ok(cli.isManagedCli(s.link));
  assert.ok(!cli.isManagedCli("/usr/bin/env"));
});

test("Update naut on the linked managed copy re-installs, no 'update it the way it was installed'", unixOnly, async () => {
  linkedOnPath();
  ui.onWarn = (_m, items) => items.find((i) => i === "Update naut");
  await cli.checkCliVersion();
  await settle();
  assert.equal(ui.progress, 1, "went straight to the install");
  assert.ok(!ui.info.some((i) => /the way it was installed/.test(i.msg)), ui.info.map((i) => i.msg).join("|"));
});

test("…while a naut of your own still gets 'update it the way it was installed'", unixOnly, async () => {
  const s = setup({ installed: false, minVersion: "9.9.9" });
  const brew = path.join(s.home, "brew", "bin");
  fakeNaut(path.join(brew, "naut"), "0.1.0");
  process.env.PATH = `${brew}${path.delimiter}${process.env.PATH}`;
  s.init();
  ui.onWarn = (_m, items) => items.find((i) => i === "Update naut");
  await cli.checkCliVersion();
  await settle();
  assert.equal(ui.progress, 0);
  assert.ok(ui.info.some((i) => /the way it was installed/.test(i.msg)));
});

test("Show CLI Info labels the linked copy 'installed by this extension'", unixOnly, async () => {
  linkedOnPath();
  await cli.showCliInfo();
  assert.match(ui.info[0].msg, /installed by this extension/);
});

test("Install over the linked copy reports it installed, not 'still using ~/.local/bin/naut'", unixOnly, async () => {
  const s = linkedOnPath();
  ui.onProgress = () => Promise.resolve({ version: "0.2.0", path: s.managed });
  await cli.installCliCommand();
  const msgs = ui.info.map((i) => i.msg);
  assert.ok(msgs.some((m) => /naut 0\.2\.0 installed\./.test(m)), msgs.join("|"));
  assert.ok(!msgs.some((m) => /still using/.test(m)), msgs.join("|"));
  assert.ok(!ui.info.some((i) => i.opts?.modal), "nothing left to set up, so no PATH prompt");
});

// ── integrated terminals run the naut the extension runs ───────────────────

test("terminals: the managed folder goes first when the extension runs the managed copy", unixOnly, () => {
  const s = setup();
  s.init();
  assert.deepEqual(s.envColl.ops, [`prepend PATH ${s.managedDir}${path.delimiter}`]);
});

test("terminals: left alone (cleared) when a naut of your own on PATH is what runs", unixOnly, () => {
  const s = setup();
  const brew = path.join(s.home, "brew", "bin");
  fakeNaut(path.join(brew, "naut"), "0.1.0");
  process.env.PATH = `${brew}${path.delimiter}${process.env.PATH}`;
  s.init();
  assert.equal(cli.resolveCliNow().command, path.join(brew, "naut"));
  assert.deepEqual(s.envColl.ops, ["clear"]);
});

test("terminals: follow nautilus.cliPath", unixOnly, () => {
  const s = setup();
  const other = path.join(s.home, "other", "naut");
  fakeNaut(other, "0.1.0");
  ui.cliPath = other;
  s.init();
  assert.deepEqual(s.envColl.ops, ["clear"]);
  ui.cliPath = s.managed;
  assert.equal(cli.syncTerminalPath(), true);
  assert.equal(s.envColl.ops.at(-1), `prepend PATH ${s.managedDir}${path.delimiter}`);
});

test("terminals: cleared when the managed copy is gone, and our dangling link removed", unixOnly, () => {
  const s = setup({ installed: false });
  fs.mkdirSync(path.dirname(s.link), { recursive: true });
  fs.symlinkSync(s.managed, s.link);
  s.init();
  assert.deepEqual(s.envColl.ops, ["clear"]);
  assert.throws(() => fs.lstatSync(s.link), "dangling link removed");
});

test("terminals: linked managed copy on PATH still gets the managed folder", unixOnly, () => {
  const s = linkedOnPath();
  assert.deepEqual(s.envColl.ops, [`prepend PATH ${s.managedDir}${path.delimiter}`]);
});

// ── nothing outside VS Code changes without the user's OK ──────────────────

test("PATH prompt: lists the files; 'Not now' changes nothing and is remembered", unixOnly, async () => {
  const s = setup();
  s.init();
  ui.onInfo = (_m, items, opts) => (opts?.modal ? "Not now" : undefined);
  const note = await cli.offerPathSetup(log, false);
  const prompt = ui.info.find((i) => i.opts?.modal);
  assert.ok(prompt, "asked");
  assert.deepEqual(prompt.items, ["Add to PATH", "Not now"]);
  assert.match(prompt.opts?.detail ?? "", /create the link ~\/\.local\/bin\/naut/);
  assert.match(prompt.opts?.detail ?? "", /create ~\/\.profile/);
  assert.match(prompt.opts?.detail ?? "", /Remove naut from PATH/);
  assert.ok(!fs.existsSync(s.link));
  assert.ok(!fs.existsSync(path.join(s.home, ".profile")));
  assert.equal(s.state.get(cli.PATH_DECLINED_KEY), true);
  assert.match(note, /Add naut to PATH/);

  ui.info = [];
  await cli.offerPathSetup(log, false);
  assert.equal(ui.info.length, 0, "the install command doesn't ask again");
  await cli.offerPathSetup(log, true);
  assert.equal(ui.info.filter((i) => i.opts?.modal).length, 1, "the explicit Add command does");
});

test("PATH prompt: a dismissed prompt changes nothing and asks again next time", unixOnly, async () => {
  const s = setup();
  s.init();
  await cli.offerPathSetup(log, false);
  assert.ok(!fs.existsSync(s.link));
  assert.equal(s.state.get(cli.PATH_DECLINED_KEY), undefined);
});

test("PATH prompt: 'Add to PATH' links and appends; Remove undoes it all", unixOnly, async () => {
  const s = setup();
  fs.writeFileSync(path.join(s.home, ".bashrc"), "# mine\n");
  s.init();
  ui.onInfo = (_m, _i, opts) => (opts?.modal ? "Add to PATH" : undefined);
  const note = await cli.offerPathSetup(log, false);
  assert.equal(fs.readlinkSync(s.link), s.managed);
  assert.match(fs.readFileSync(path.join(s.home, ".bashrc"), "utf8"), /export PATH="\$PATH:\$HOME\/\.local\/bin"/);
  assert.match(note, /~\/\.profile, ~\/\.bashrc/);
  assert.match(note, /Remove naut from PATH/);

  await cli.removeCliFromPathCommand();
  assert.throws(() => fs.lstatSync(s.link));
  assert.equal(fs.readFileSync(path.join(s.home, ".bashrc"), "utf8"), "# mine\n");
  assert.equal(s.envColl.ops.at(-1), "clear");
  assert.equal(cli.syncTerminalPath(), false, "terminals stay off after Remove");
  assert.equal(s.envColl.ops.at(-1), "clear");

  ui.info = [];
  await cli.addCliToPathCommand();
  assert.equal(s.envColl.ops.at(-1), `prepend PATH ${s.managedDir}${path.delimiter}`, "Add turns terminals back on");
  assert.ok(fs.existsSync(s.link));
});

test("PATH prompt: never shown when a naut of your own is what runs", unixOnly, async () => {
  const s = setup();
  const brew = path.join(s.home, "brew", "bin");
  fakeNaut(path.join(brew, "naut"), "0.1.0");
  process.env.PATH = `${brew}${path.delimiter}${process.env.PATH}`;
  s.init();
  ui.onInfo = () => "Add to PATH";
  assert.equal(await cli.offerPathSetup(log, false), "");
  assert.equal(ui.info.length, 0);
  assert.ok(!fs.existsSync(s.link));
  assert.ok(!fs.existsSync(path.join(s.home, ".profile")));
});
