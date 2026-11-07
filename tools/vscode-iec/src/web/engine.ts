// The extension's side of the playground engine bridge. The playground page
// runs nautilus (naut.wasm) in a worker and serves it on a BroadcastChannel
// named in the nautilus.playground.channel setting
// (playground/src/bridge.js); this is the client.
import * as vscode from "vscode";

type Pending = { resolve: (v: unknown) => void; reject: (e: Error) => void };

/** The base URL the extension's controller code is pointed at in the
 * playground; requests to it are served by the engine, not the network. */
export const PLAYGROUND_URL = "http://playground.nautilus";

class Engine {
  private ch: BroadcastChannel | null = null;
  private seq = 0;
  private pending = new Map<number, Pending>();
  private streams = new Map<number, (chunk: string | null) => void>();
  readonly lspListeners = new Set<(msg: unknown) => void>();
  readonly fsListeners = new Set<(path: string, kind: string) => void>();

  private channel(): BroadcastChannel {
    if (this.ch) return this.ch;
    const name = vscode.workspace.getConfiguration("nautilus").get<string>("playground.channel", "");
    if (!name) throw new Error("nautilus: no playground engine (nautilus.playground.channel is not set)");
    const ch = new BroadcastChannel(name);
    ch.onmessage = ({ data: m }) => {
      switch (m?.kind) {
        case "result": {
          const p = this.pending.get(m.id);
          if (!p) return;
          this.pending.delete(m.id);
          if (m.ok) p.resolve(m.result);
          else p.reject(new Error(m.error));
          return;
        }
        case "chunk": {
          const s = this.streams.get(m.sid);
          s?.(m.chunk);
          if (m.chunk === null) this.streams.delete(m.sid);
          return;
        }
        case "lsp":
          for (const l of this.lspListeners) l(m.msg);
          return;
        case "fs":
          for (const l of this.fsListeners) l(m.path, m.fsKind);
          return;
      }
    };
    this.ch = ch;
    return ch;
  }

  available(): boolean {
    return !!vscode.workspace.getConfiguration("nautilus").get<string>("playground.channel", "");
  }

  call<T = unknown>(op: string, ...args: unknown[]): Promise<T> {
    const ch = this.channel();
    const id = ++this.seq;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject });
      ch.postMessage({ kind: "call", id, op, args });
    });
  }

  /** Raw text of a streaming endpoint; onChunk(null) at its end. */
  stream(url: string, onChunk: (chunk: string | null) => void): () => void {
    const ch = this.channel();
    const sid = ++this.seq;
    this.streams.set(sid, onChunk);
    ch.postMessage({ kind: "stream", sid, url });
    return () => {
      this.streams.delete(sid);
      ch.postMessage({ kind: "streamCancel", sid });
    };
  }

  lspSend(msg: unknown): void {
    this.channel().postMessage({ kind: "lsp", msg });
  }

  /** The tag API as fetch would answer it. */
  async fetch(url: string, init: RequestInit = {}): Promise<Response> {
    const u = new URL(url);
    const headers: Record<string, string> = {};
    new Headers(init.headers ?? {}).forEach((v, k) => (headers[k] = v));
    const r = await this.call<{ status: number; headers: Record<string, string>; body: string }>(
      "call",
      "handle",
      (init.method ?? "GET").toUpperCase(),
      u.pathname + u.search,
      headers,
      init.body == null ? null : String(init.body)
    );
    return new Response(r.status === 204 || r.status === 304 ? null : r.body, { status: r.status, headers: r.headers });
  }
}

export const engine = new Engine();

/** True for a URL the engine answers. */
export function isPlaygroundUrl(url: string | URL): boolean {
  return String(url).startsWith(PLAYGROUND_URL);
}

let fetchPatched = false;
/** Route fetch() calls aimed at the playground controller to the engine:
 * the extension's controller code (online edit, forces, SFC commands,
 * connect) calls fetch(runtimeUrl + "/api/..."), unchanged. */
export function patchFetch(): void {
  if (fetchPatched) return;
  fetchPatched = true;
  const real = globalThis.fetch.bind(globalThis);
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" || input instanceof URL ? String(input) : input.url;
    if (isPlaygroundUrl(url)) return engine.fetch(url, init);
    return real(input, init);
  }) as typeof fetch;
}
