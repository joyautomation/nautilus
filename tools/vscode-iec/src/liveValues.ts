// Inline live tag values: decorate identifiers in .st and .fbd editors with
// the current value from a running nautilus controller.
//
// Data path: the nautilus `server` package broadcasts tag frames over SSE
// (GET <runtimeUrl>/api/stream, `data: {"ts":..,"scans":..,"tags":{...}}`).
// This module keeps one subscription alive while enabled, and re-renders
// decorations on every frame. Identifier scanning skips `//` and `(* *)`
// comments and string literals, and matches tag names case-insensitively
// (IEC identifiers are case-insensitive; the runtime keys by declared
// casing).

import * as http from "http";
import * as https from "https";
import * as vscode from "vscode";
import { projectDirFor, projectFiles } from "./projectFiles";
import {
  formatValue,
  formatValueHover,
  instanceScope,
  parseWriteValue,
  scanFbRegions,
  scanIdentifiers,
  scanInstanceDecls,
} from "./scan";

type Frame = {
  ts: number;
  scans: number;
  tags: Record<string, unknown>;
  // Retained program locals (a PI integral, latches, FB instances with
  // their pins) — the watch inside the POU, streamed alongside the tags.
  locals?: Record<string, unknown>;
};

/** All four IEC languages get live values — the identifier scanner is syntax-
 * agnostic and every source form references the same runtime tags. */
function isIecDoc(doc: vscode.TextDocument): boolean {
  return doc.languageId === "iec-st" || doc.languageId === "iec-fbd" ||
    doc.languageId === "iec-ld" || doc.languageId === "iec-sfc";
}

/** A frame is "fresh" if it arrived within this window; otherwise chips gray out. */
const FRESHNESS_MS = 3000;
const RECONNECT_MS = 2000;
const RENDER_THROTTLE_MS = 150;

/** A frame fanned out to non-text consumers (the FBD diagram webviews). */
export type LiveFrameListener = (frame: {
  enabled: boolean;
  fresh: boolean;
  values: Record<string, unknown>;
}) => void;

export class LiveValues implements vscode.Disposable {
  private enabled: boolean;
  private values = new Map<string, unknown>(); // lowercased tag name → value
  // Last frame's tags and locals with their DECLARED casing, for the Live
  // Values panel (the decoration path lowercases; a list wants real names).
  private lastTags: [string, unknown][] = [];
  private lastLocals: [string, unknown][] = [];
  private readonly valuesChanged = new vscode.EventEmitter<void>();
  /** Fires when the snapshot changes (a frame arrived, or the stream went
   * stale/offline) — the Live Values view refreshes on this. */
  readonly onDidChangeValues = this.valuesChanged.event;
  private listeners = new Set<LiveFrameListener>();
  // FUNCTION_BLOCK instance monitoring: which called instance an FB body's
  // pills read from (fb type, lowercased → instance name), plus the
  // instances declared across project sources (found by scanning).
  private monitors = new Map<string, string>();
  private candidates = new Map<string, string[]>();
  private candidatesAt = 0;
  private readonly monitorsChanged = new vscode.EventEmitter<void>();
  readonly onDidChangeMonitors = this.monitorsChanged.event;
  private lastFrameMs = 0;
  private req: http.ClientRequest | undefined;
  private reconnectTimer: NodeJS.Timeout | undefined;
  private staleTimer: NodeJS.Timeout;
  private renderTimer: NodeJS.Timeout | undefined;
  private disposables: vscode.Disposable[] = [];

  private readonly freshDeco = pillDecoration(
    new vscode.ThemeColor("charts.green"),
    "rgba(100, 216, 138, 0.13)",
    "rgba(100, 216, 138, 0.38)"
  );
  private readonly staleDeco = pillDecoration(
    new vscode.ThemeColor("descriptionForeground"),
    "rgba(140, 140, 140, 0.12)",
    "rgba(140, 140, 140, 0.32)"
  );
  private readonly status = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Right,
    90
  );

  constructor() {
    this.enabled = this.configEnabled();
    this.status.command = "nautilus.liveValues.toggle";
    this.staleTimer = setInterval(() => this.onStaleCheck(), 1000);
    this.disposables.push(
      vscode.window.onDidChangeVisibleTextEditors(() => this.onEditorsChanged()),
      vscode.workspace.onDidChangeTextDocument((e) => {
        if (isIecDoc(e.document)) this.scheduleRender();
      })
    );
    this.onEditorsChanged();
  }

  toggle(): void {
    this.enabled = !this.enabled;
    // Write to the scope that actually governs the effective value. A
    // scaffolded project pins liveValues.enabled in workspace
    // .vscode/settings.json, which overrides a Global write — so toggling to
    // Global would be immediately reverted by configChanged() re-reading the
    // workspace value. Target Workspace when a folder is open, else Global.
    const target = vscode.workspace.workspaceFolders?.length
      ? vscode.ConfigurationTarget.Workspace
      : vscode.ConfigurationTarget.Global;
    void vscode.workspace
      .getConfiguration("nautilus")
      .update("liveValues.enabled", this.enabled, target);
    this.onEditorsChanged();
  }

  configChanged(): void {
    this.enabled = this.configEnabled();
    this.disconnect();
    this.onEditorsChanged();
  }

  private configEnabled(): boolean {
    return vscode.workspace
      .getConfiguration("nautilus")
      .get<boolean>("liveValues.enabled", true);
  }

  private runtimeUrl(): string {
    return vscode.workspace
      .getConfiguration("nautilus")
      .get<string>("runtimeUrl", "http://localhost:8080")
      .replace(/\/+$/, "");
  }

  /** The last streamed value of a top-level tag, or undefined if none has
   * arrived (no controller, or a name that isn't a tag). Keyed like the
   * cache: case-insensitive, top-level names only. */
  valueFor(name: string): unknown {
    return this.values.get(name.toLowerCase());
  }

  /**
   * Write a value to a tag over the API — the editor's counterpart to the
   * dashboard's tag table (POST /api/tags). tag defaults to the identifier
   * under the active cursor, so this serves both the right-click command and
   * the "Set value…" link in a pill's hover. The value is parsed as the pill
   * shows it: a bare number, or TRUE/FALSE. A written setpoint sticks; a
   * state/output tag the scan owns is overwritten next cycle — that's honest
   * PLC forcing, not a bug. The pill updates on the next frame.
   */
  async setValue(arg?: string | { tag?: string; name?: string }): Promise<void> {
    // arg is a tag name (hover link), a Live Values tree item ({tag}), or
    // undefined (palette / right-click → read the identifier under the cursor).
    const tag = typeof arg === "string" ? arg : arg?.tag ?? arg?.name;
    const name = tag ?? this.identifierAtCursor();
    if (!name) {
      void vscode.window.showWarningMessage("nautilus: put the cursor on a tag, then Set Live Value");
      return;
    }
    const current = this.valueFor(name);
    const prefill = current === undefined ? "" : formatValue(current);
    const input = await vscode.window.showInputBox({
      title: `nautilus: Set ${name}`,
      prompt: current === undefined ? "New value (number, or TRUE/FALSE)" : `Current ${formatValue(current)} — new value (number, or TRUE/FALSE)`,
      value: prefill,
      validateInput: (v) => (parseWriteValue(v) === undefined ? "Enter a number, or TRUE/FALSE" : undefined),
    });
    if (input === undefined) return;
    const value = parseWriteValue(input);
    if (value === undefined) return;

    const headers: Record<string, string> = { "Content-Type": "application/json" };
    const token = vscode.workspace.getConfiguration("nautilus").get<string>("token", "");
    if (token) headers["Authorization"] = "Bearer " + token;
    try {
      const res = await fetch(this.runtimeUrl() + "/api/tags", {
        method: "POST",
        headers,
        body: JSON.stringify({ name, value }),
      });
      if (res.status === 204 || res.ok) {
        void vscode.window.showInformationMessage(`nautilus: set ${name} = ${formatValue(value)}`);
        return;
      }
      const body = await res.text();
      void vscode.window.showErrorMessage(`nautilus: set ${name} rejected — ${body.trim() || res.statusText}`);
    } catch (e) {
      void vscode.window.showErrorMessage(`nautilus: could not reach ${this.runtimeUrl()} — ${String(e)}`);
    }
  }

  /** The bare identifier under the active editor's cursor, or "". Only the
   * top-level watch is offered a value write, so an FB member path (a name
   * with a dot) resolves to "" here. */
  private identifierAtCursor(): string {
    const editor = vscode.window.activeTextEditor;
    if (!editor || !isIecDoc(editor.document)) return "";
    const range = editor.document.getWordRangeAtPosition(editor.selection.active, /[A-Za-z_][A-Za-z0-9_]*/);
    return range ? editor.document.getText(range) : "";
  }

  private stEditors(): vscode.TextEditor[] {
    return vscode.window.visibleTextEditors.filter((e) => isIecDoc(e.document));
  }

  /** The diagram webviews consume frames too: while any are open, keep the
   * stream alive even with no text editor visible, and push each frame (and
   * enabled-state changes) to them. */
  addConsumer(listener: LiveFrameListener): vscode.Disposable {
    this.listeners.add(listener);
    this.onEditorsChanged();
    this.notify(); // current state immediately
    return new vscode.Disposable(() => {
      this.listeners.delete(listener);
      this.onEditorsChanged();
    });
  }

  private notify(): void {
    if (this.listeners.size === 0) return;
    const frame = {
      enabled: this.enabled,
      fresh: this.fresh(),
      values: Object.fromEntries(this.values),
    };
    for (const l of this.listeners) l(frame);
  }

  /** Connect only while enabled and an ST editor is visible. */
  private onEditorsChanged(): void {
    const wanted = this.enabled && (this.stEditors().length > 0 || this.listeners.size > 0);
    if (wanted && !this.req) this.connect();
    if (!wanted) this.disconnect();
    this.updateStatus();
    this.scheduleRender();
  }

  // ── SSE subscription ──────────────────────────────────────────────────

  private connect(): void {
    const url = new URL(this.runtimeUrl() + "/api/stream");
    const mod = url.protocol === "https:" ? https : http;
    let buffer = "";

    const req = mod.get(url, (res) => {
      if (res.statusCode !== 200) {
        res.resume();
        this.scheduleReconnect();
        return;
      }
      res.setEncoding("utf8");
      res.on("data", (chunk: string) => {
        buffer += chunk;
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const event = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          for (const line of event.split("\n")) {
            if (line.startsWith("data: ")) this.onFrame(line.slice(6));
          }
        }
      });
      res.on("end", () => this.scheduleReconnect());
      // A controller that dies mid-stream cuts a chunked response short:
      // Node reports that as 'aborted'/'close' (and an 'error' on the
      // response), never 'end'. Without these the client sat on a dead
      // socket forever — pills grey, no reconnect — until live values
      // were toggled. scheduleReconnect is idempotent, so overlapping
      // events are harmless.
      res.on("error", () => this.scheduleReconnect());
      res.on("close", () => this.scheduleReconnect());
    });
    req.on("error", () => this.scheduleReconnect());
    req.on("close", () => this.scheduleReconnect());
    this.req = req;
  }

  private disconnect(): void {
    this.req?.destroy();
    this.req = undefined;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
  }

  private scheduleReconnect(): void {
    this.req = undefined;
    if (this.reconnectTimer || !this.enabled) return;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      this.onEditorsChanged();
    }, RECONNECT_MS);
  }

  private onFrame(payload: string): void {
    let frame: Frame;
    try {
      frame = JSON.parse(payload) as Frame;
    } catch {
      return;
    }
    this.values.clear();
    // Locals first so a tag of the same name wins (globals shadow locals in
    // the merged watch — the rare collision resolves to the bound value).
    for (const [name, value] of Object.entries(frame.locals ?? {})) {
      this.values.set(name.toLowerCase(), value);
    }
    for (const [name, value] of Object.entries(frame.tags ?? {})) {
      this.values.set(name.toLowerCase(), value);
    }
    this.lastTags = Object.entries(frame.tags ?? {});
    this.lastLocals = Object.entries(frame.locals ?? {});
    const wasStale = !this.fresh();
    this.lastFrameMs = Date.now();
    if (wasStale) this.updateStatus();
    this.scheduleRender();
    this.valuesChanged.fire();
  }

  /** The current tags and locals with declared casing, for the Live Values
   * panel. tags are settable (POST /api/tags by name); locals are the
   * program's retained internals and are read-only here. */
  snapshot(): { tags: [string, unknown][]; locals: [string, unknown][]; enabled: boolean; fresh: boolean } {
    return { tags: this.lastTags, locals: this.lastLocals, enabled: this.enabled, fresh: this.fresh() };
  }

  private fresh(): boolean {
    return Date.now() - this.lastFrameMs < FRESHNESS_MS;
  }

  private onStaleCheck(): void {
    // Flip chips to the stale style (and the status bar to offline) when
    // frames stop arriving; no re-render needed while nothing changes.
    if (!this.fresh() && this.values.size > 0) {
      this.updateStatus();
      this.scheduleRender();
      this.valuesChanged.fire(); // grey the panel too
    }
    // Watchdog: a socket that is open but silent (cable pulled, controller
    // wedged — no FIN ever arrives) would otherwise never be retried. Two
    // freshness windows of silence with a request outstanding means the
    // connection is dead whatever TCP thinks; tear it down and let the
    // normal retry find the controller again.
    if (this.req && this.lastFrameMs > 0 && Date.now() - this.lastFrameMs > 2 * FRESHNESS_MS) {
      this.req.destroy();
      this.scheduleReconnect();
    }
  }

  // ── Rendering ─────────────────────────────────────────────────────────

  private scheduleRender(): void {
    if (this.renderTimer) return;
    this.renderTimer = setTimeout(() => {
      this.renderTimer = undefined;
      this.render();
    }, RENDER_THROTTLE_MS);
  }

  private render(): void {
    const fresh = this.fresh();
    for (const editor of this.stEditors()) {
      if (!this.enabled || this.values.size === 0) {
        editor.setDecorations(this.freshDeco, []);
        editor.setDecorations(this.staleDeco, []);
        continue;
      }
      const decos: vscode.DecorationOptions[] = [];
      const text = editor.document.getText();
      // Segment the document: program text scans against the global watch;
      // each FUNCTION_BLOCK body scans against its MONITORED instance's
      // members (r1.prev behind a bare `prev`), like a PLC IDE's instance
      // view. Bodies without a monitored (or streaming) instance show no
      // pills — a bare member has no value of its own.
      type Seg = { text: string; offset: number; map: ReadonlyMap<string, unknown>; prefix: string };
      const segs: Seg[] = [];
      let cursor = 0;
      const regions = scanFbRegions(text);
      for (const r of regions) {
        if (r.start > cursor) {
          segs.push({ text: text.slice(cursor, r.start), offset: cursor, map: this.values, prefix: "" });
        }
        const inst = this.monitorFor(r.type);
        const scoped = inst ? instanceScope(this.values, inst) : undefined;
        if (inst && scoped) {
          segs.push({ text: text.slice(r.bodyStart, r.end), offset: r.bodyStart, map: scoped, prefix: inst + "." });
        }
        cursor = r.end;
      }
      if (cursor < text.length) {
        segs.push({ text: text.slice(cursor), offset: cursor, map: this.values, prefix: "" });
      }
      if (regions.length > 0) this.ensureCandidates(editor.document);

      for (const seg of segs) {
        for (const site of scanIdentifiers(seg.text, seg.map)) {
          const pos = editor.document.positionAt(seg.offset + site.end);
          // site.value is resolved down the accessor path — a member reference
          // (RTU.VALUE) shows the child value, not the parent struct.
          const hover = new vscode.MarkdownString();
          hover.appendMarkdown(`**${seg.prefix}${site.path}** — live value from ${this.runtimeUrl()}\n`);
          hover.appendCodeblock(formatValueHover(site.value), "");
          // "Set value…" — only for a bare top-level tag (no FB-instance
          // prefix, no member/index path); a struct member isn't a writable
          // name on its own. The command link needs a trusted hover.
          if (seg.prefix === "" && /^[A-Za-z_][A-Za-z0-9_]*$/.test(site.path)) {
            const arg = encodeURIComponent(JSON.stringify([site.path]));
            hover.appendMarkdown(`\n\n[$(edit) Set value…](command:nautilus.setValue?${arg})`);
            hover.isTrusted = { enabledCommands: ["nautilus.setValue"] };
            hover.supportThemeIcons = true;
          }
          decos.push({
            range: new vscode.Range(pos, pos),
            renderOptions: {
              after: { contentText: formatValue(site.value) },
            },
            hoverMessage: hover,
          });
        }
      }
      editor.setDecorations(fresh ? this.staleDeco : this.freshDeco, []);
      editor.setDecorations(fresh ? this.freshDeco : this.staleDeco, decos);
    }
    this.notify();
  }

  // ── FUNCTION_BLOCK instance monitoring ──────────────────────────────────

  /** The instance an FB type's body currently reads from ("" = none). */
  monitorFor(fbType: string): string {
    return this.monitors.get(fbType.toLowerCase()) ?? "";
  }

  /** Candidate instances of an FB type declared across the project. */
  candidatesFor(fbType: string): string[] {
    return this.candidates.get(fbType.toLowerCase()) ?? [];
  }

  /** Point an FB type's body at a called instance and re-render. */
  setMonitor(fbType: string, instance: string): void {
    this.monitors.set(fbType.toLowerCase(), instance);
    this.monitorsChanged.fire();
    this.scheduleRender();
  }

  /** Refresh the instance-declaration index from the project's sources
   * (at most every few seconds); auto-monitor types with exactly one
   * declared instance, so the common case needs no clicks at all. */
  private ensureCandidates(doc: vscode.TextDocument): void {
    const now = Date.now();
    if (now - this.candidatesAt < 5000) return;
    this.candidatesAt = now;
    void (async () => {
      let sources = "";
      try {
        // The project's root and lib/ files: instances are declared in
        // programs (root) and inside other blocks (often lib/).
        const root = await projectDirFor(doc.uri);
        for (const { uri } of await projectFiles(root, /\.(st|fbd)$/i)) {
          const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
          sources += (open ? open.getText() : new TextDecoder().decode(await vscode.workspace.fs.readFile(uri))) + "\n";
        }
      } catch {
        return;
      }
      let changed = false;
      for (const editor of this.stEditors()) {
        for (const r of scanFbRegions(editor.document.getText())) {
          const key = r.type.toLowerCase();
          const found = scanInstanceDecls(sources, r.type);
          const prev = this.candidates.get(key) ?? [];
          if (found.join("|") !== prev.join("|")) changed = true;
          this.candidates.set(key, found);
          if (!this.monitors.has(key) && found.length === 1) {
            this.monitors.set(key, found[0]);
            changed = true;
          }
        }
      }
      if (changed) {
        this.monitorsChanged.fire();
        this.scheduleRender();
      }
    })();
  }

  /** The "monitor instance…" command: QuickPick among declared instances. */
  async pickMonitor(fbType: string): Promise<void> {
    this.candidatesAt = 0; // force a fresh scan next render
    const current = this.monitorFor(fbType);
    const names = this.candidatesFor(fbType);
    if (names.length === 0) {
      void vscode.window.showInformationMessage(
        `nautilus: no declared instances of ${fbType} found in this project's sources`
      );
      return;
    }
    const pick = await vscode.window.showQuickPick(
      names.map((n) => ({
        label: n,
        description: n === current ? "monitoring" : undefined,
      })),
      { title: `Monitor which ${fbType} instance?` }
    );
    if (pick) this.setMonitor(fbType, pick.label);
  }

  private updateStatus(): void {
    // Visible while anything shows live values — a text editor OR a diagram
    // webview (the diagram's toolbar toggle drives the same command).
    if (this.stEditors().length === 0 && this.listeners.size === 0) {
      this.status.hide();
      return;
    }
    if (!this.enabled) {
      this.status.text = "$(circle-slash) nautilus: live values off";
      this.status.tooltip = "Click to enable inline live tag values";
    } else if (this.fresh()) {
      this.status.text = "$(pulse) nautilus: live";
      this.status.tooltip = `Streaming tag values from ${this.runtimeUrl()} — click to disable`;
    } else {
      this.status.text = "$(debug-disconnect) nautilus: offline";
      this.status.tooltip = `No frames from ${this.runtimeUrl()}/api/stream — is the controller running?`;
    }
    this.status.show();
  }

  dispose(): void {
    this.disconnect();
    clearInterval(this.staleTimer);
    if (this.renderTimer) clearTimeout(this.renderTimer);
    this.freshDeco.dispose();
    this.staleDeco.dispose();
    this.status.dispose();
    this.monitorsChanged.dispose();
    for (const d of this.disposables) d.dispose();
  }
}

/** CodeLens over each FUNCTION_BLOCK header: which called instance the
 * body's live pills read from, and the affordance to switch — the
 * PLC-IDE "open instance" experience. */
export class FbMonitorLenses implements vscode.CodeLensProvider {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this.changed.event;

  constructor(private live: LiveValues) {
    live.onDidChangeMonitors(() => this.changed.fire());
  }

  provideCodeLenses(doc: vscode.TextDocument): vscode.CodeLens[] {
    const out: vscode.CodeLens[] = [];
    for (const r of scanFbRegions(doc.getText())) {
      const inst = this.live.monitorFor(r.type);
      const n = this.live.candidatesFor(r.type).length;
      const title = inst
        ? `◉ live values: monitoring ${inst}${n > 1 ? ` — 1 of ${n}, click to switch` : ""}`
        : "○ live values: monitor an instance…";
      out.push(
        new vscode.CodeLens(new vscode.Range(r.headerLine, 0, r.headerLine, 0), {
          title,
          command: "nautilus.fb.monitor",
          arguments: [r.type],
        })
      );
    }
    return out;
  }
}

// pillDecoration builds a rounded "pill" attachment so live values read as an
// overlay, not as part of the source. VS Code's decoration API exposes color,
// background, border, and weight as typed fields but has no border-radius or
// padding; it applies the `textDecoration` string as raw CSS on the ::after
// box, so the pill shape is smuggled through there (the leading `none;`
// terminates the text-decoration declaration).
function pillDecoration(
  color: vscode.ThemeColor,
  background: string,
  border: string
): vscode.TextEditorDecorationType {
  return vscode.window.createTextEditorDecorationType({
    after: {
      margin: "0 0 0 0.6em",
      color,
      backgroundColor: background,
      border: `1px solid ${border}`,
      fontWeight: "600",
      textDecoration:
        "none; border-radius: 5px; padding: 0px 5px; font-size: 0.85em; vertical-align: baseline;",
    },
  });
}

