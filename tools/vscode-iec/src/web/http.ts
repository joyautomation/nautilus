// http/https for the web build: the live-values SSE subscription
// (liveValues.ts: mod.get(runtimeUrl + "/api/stream")) reads the playground
// engine's real stream. Other hosts have no raw sockets in a browser.
import { engine, isPlaygroundUrl } from "./engine";

type Handler = (...a: unknown[]) => void;
class Emitter {
  private h = new Map<string, Handler[]>();
  on(ev: string, fn: Handler): this {
    this.h.set(ev, [...(this.h.get(ev) ?? []), fn]);
    return this;
  }
  once(ev: string, fn: Handler): this {
    return this.on(ev, fn);
  }
  emit(ev: string, ...a: unknown[]): void {
    for (const fn of this.h.get(ev) ?? []) fn(...a);
  }
}

export function get(url: string | URL, opts?: unknown, cb?: unknown) {
  const callback = (typeof opts === "function" ? opts : cb) as ((res: unknown) => void) | undefined;
  const req = new Emitter() as Emitter & { destroy(): void };
  let stop: (() => void) | null = null;
  let closed = false;
  req.destroy = () => {
    if (closed) return;
    closed = true;
    stop?.();
  };
  setTimeout(() => {
    if (closed) return;
    if (!isPlaygroundUrl(url)) {
      req.emit("error", new Error(`${url}: the browser playground reaches only its own controller`));
      req.emit("close");
      return;
    }
    const res = Object.assign(new Emitter(), { statusCode: 200, setEncoding() {}, resume() {} });
    callback?.(res);
    const u = new URL(String(url));
    stop = engine.stream(u.pathname + u.search, (chunk) => {
      if (chunk === null) {
        res.emit("end");
        res.emit("close");
        return;
      }
      res.emit("data", chunk);
    });
  }, 0);
  return req;
}
export const request = get;
export default { get, request };
