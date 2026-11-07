// child_process for the web build: the extension's execFile(naut, args)
// calls run the same verbs in the playground engine (naut.wasm's exec:
// compose, ld|fbd|sfc graph|edit, test -json|-list), with the same stdout,
// so ldPreview, fbdPreview, acceptanceTests... are unchanged. Anything that
// is not `naut` (git) fails the way a missing program does.
import { engine } from "./engine";

type Cb = (err: (Error & { code?: number | string }) | null, stdout: string, stderr: string) => void;

class Stream {
  private chunks: string[] = [];
  constructor(private onEnd: () => void) {}
  touched = false;
  write(d: unknown): boolean {
    this.touched = true;
    this.chunks.push(String(d));
    return true;
  }
  end(d?: unknown): void {
    this.touched = true;
    if (d != null) this.chunks.push(String(d));
    this.onEnd();
  }
  text(): string {
    return this.chunks.join("");
  }
  on(): this {
    return this;
  }
}

function emitter() {
  return { on: () => emitter(), once: () => emitter() };
}

export function execFile(file: string, args?: unknown, opts?: unknown, cb?: unknown) {
  let argv: string[] = [];
  let options: { cwd?: string } = {};
  let callback: Cb | undefined;
  for (const a of [args, opts, cb]) {
    if (typeof a === "function") callback = a as Cb;
    else if (Array.isArray(a)) argv = a.map(String);
    else if (a && typeof a === "object") options = a as { cwd?: string };
  }
  let started = false;
  const run = () => {
    if (started) return;
    started = true;
    const base = String(file).split(/[\\/]/).pop()?.replace(/\.exe$/, "");
    if (base !== "naut") {
      const e = Object.assign(new Error(`spawn ${file} ENOENT`), { code: "ENOENT" });
      callback?.(e, "", "");
      return;
    }
    engine
      .call<{ stdout: string; stderr: string; code: number }>("exec", argv, stdin.text(), options.cwd ?? "/project")
      .then((r) => {
        const err = r.code === 0 ? null : Object.assign(new Error(`naut ${argv.join(" ")} exited ${r.code}: ${r.stderr}`), { code: r.code });
        callback?.(err, r.stdout, r.stderr);
      })
      .catch((e) => callback?.(e instanceof Error ? e : new Error(String(e)), "", ""));
  };
  const stdin = new Stream(run);
  // A call that never writes stdin runs on the next turn; one that does
  // runs when it ends its input.
  setTimeout(() => {
    if (!stdin.touched) run();
  }, 0);
  return { stdin, stdout: emitter(), stderr: emitter(), on: () => emitter(), kill: () => true, pid: -1 };
}

export function spawn(file: string): never {
  throw Object.assign(new Error(`spawn ${file}: not available in the browser playground`), { code: "ENOENT" });
}
export const exec = spawn;
export const execSync = spawn;
export const execFileSync = spawn;
export const spawnSync = spawn;
export type ExecFileOptionsWithStringEncoding = { cwd?: string; timeout?: number; maxBuffer?: number; encoding?: string };
export default { execFile, spawn, exec, execSync, execFileSync, spawnSync };
