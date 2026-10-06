// The running language client, for modules that send it requests of their
// own (diagramXref.ts asks `nautilus/descriptions`). extension.ts owns the
// client's lifecycle and reports it here; a caller that arrives before the
// server is up (a diagram opened at startup) waits for it, briefly.

type Requester = { sendRequest<R>(method: string, params: unknown): Promise<R> };

let current: Requester | undefined;
let waiters: ((c: Requester | undefined) => void)[] = [];

/** extension.ts: the client started (or, with undefined, stopped). */
export function setLspClient(c: Requester | undefined): void {
  current = c;
  if (!c) return;
  const ws = waiters;
  waiters = [];
  for (const w of ws) w(c);
}

/** The client once it is running; undefined if it is not up within
 * `timeoutMs` (no CLI, or a server that failed to start). */
export function lspClient(timeoutMs = 15000): Promise<Requester | undefined> {
  if (current) return Promise.resolve(current);
  return new Promise((resolve) => {
    const w = (c: Requester | undefined) => {
      clearTimeout(timer);
      resolve(c);
    };
    const timer = setTimeout(() => {
      waiters = waiters.filter((x) => x !== w);
      resolve(undefined);
    }, timeoutMs);
    waiters.push(w);
  });
}
