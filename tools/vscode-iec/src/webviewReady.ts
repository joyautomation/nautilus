// The ready handshake for the FBD / Ladder / SFC diagram webviews.
//
// A webview's listener only exists once its bundle has run, so a message
// posted right after `webview.html = …` — the first model, the live-values
// consumer's first frame, the sync pill — can land before anyone hears it
// and is simply lost (the live pill then never shows). The mimic editor
// fixed this with a `ready` message; this is the same contract for the
// diagram bundle, applied WITHOUT touching each host's many post sites:
//
//   - gateWebview() wraps the webview's postMessage. Until the page says
//     `{type:'ready'}`, messages are held instead of sent.
//   - State messages are remembered as the latest of their kind (model/
//     diff, error, diagnostics, syncState, liveValues). On `ready` the
//     latest of each is replayed in that order; anything else posted
//     before the first `ready` is delivered once, after them.
//   - The page posts `ready` on EVERY mount, so a reload (a hidden panel
//     restored without retained context, a devtools reload) replays the
//     current state the same way, without the host noticing the reload.
//
// buildWebviewHtml() calls it, so every diagram panel is gated the moment
// it gets its html.
import type * as vscode from "vscode";

type Msg = { type?: unknown };

/** Replay order: the view first, then what decorates it. */
const ORDER = ["view", "error", "diagnostics", "syncState", "liveValues"];

/** The replay slot a state message occupies: model and diff views share
 * one (whichever came last is what the panel shows); an error is kept
 * beside it — it annotates the last good model rather than replacing it.
 * Undefined for anything that isn't state (one-shot messages). */
export function slotOf(msg: unknown): string | undefined {
  const t = typeof (msg as Msg)?.type === "string" ? ((msg as Msg).type as string) : "";
  if (/^(model|diff|ldModel|ldDiff|sfcModel|sfcDiff)$/.test(t)) return "view";
  return ORDER.includes(t) ? t : undefined;
}

export class ReadyGate {
  private ready = false;
  private latest = new Map<string, unknown>();
  private pending: unknown[] = [];

  constructor(private readonly send: (msg: unknown) => Thenable<boolean>) {}

  /** A host post: remembered, and sent only once the page is listening. */
  post(msg: unknown): Thenable<boolean> {
    const slot = slotOf(msg);
    // A fresh view supersedes an earlier error; an error stays until the
    // next good view.
    if (slot === "view") this.latest.delete("error");
    if (slot) this.latest.set(slot, msg);
    if (this.ready) return this.send(msg);
    if (!slot) this.pending.push(msg);
    return Promise.resolve(true);
  }

  /** The page (re)mounted: open the gate and replay the latest state. */
  onReady(): void {
    this.ready = true;
    const once = this.pending;
    this.pending = [];
    for (const m of [...this.replay(), ...once]) void this.send(m);
  }

  /** New html is loading: hold messages until it says ready. */
  reset(): void {
    this.ready = false;
  }

  /** The latest state, in replay order. */
  replay(): unknown[] {
    return ORDER.filter((k) => this.latest.has(k)).map((k) => this.latest.get(k));
  }
}

const gates = new WeakMap<vscode.Webview, ReadyGate>();

/** Gate a diagram webview's postMessage on the page's `ready`. Idempotent
 * per webview; calling it again (new html) re-arms the gate. */
export function gateWebview(webview: vscode.Webview): void {
  const existing = gates.get(webview);
  if (existing) {
    existing.reset();
    return;
  }
  const original = webview.postMessage.bind(webview);
  const gate = new ReadyGate(original);
  gates.set(webview, gate);
  webview.postMessage = (msg: unknown) => gate.post(msg);
  // Disposed with the webview (its event emitter goes with it).
  webview.onDidReceiveMessage((msg: Msg) => {
    if (msg?.type === "ready") gate.onReady();
  });
}
