// Inline live tag values: decorate identifiers in .st editors with the
// current value from a running nautilus controller.
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
import { formatValue, scanIdentifiers } from "./scan";

type Frame = { ts: number; scans: number; tags: Record<string, unknown> };

/** A frame is "fresh" if it arrived within this window; otherwise chips gray out. */
const FRESHNESS_MS = 3000;
const RECONNECT_MS = 2000;
const RENDER_THROTTLE_MS = 150;

export class LiveValues implements vscode.Disposable {
  private enabled: boolean;
  private values = new Map<string, unknown>(); // lowercased tag name → value
  private casing = new Map<string, string>(); // lowercased → declared casing
  private lastFrameMs = 0;
  private req: http.ClientRequest | undefined;
  private reconnectTimer: NodeJS.Timeout | undefined;
  private staleTimer: NodeJS.Timeout;
  private renderTimer: NodeJS.Timeout | undefined;
  private disposables: vscode.Disposable[] = [];

  private readonly freshDeco = vscode.window.createTextEditorDecorationType({
    after: {
      margin: "0 0 0 0.75em",
      color: new vscode.ThemeColor("charts.green"),
    },
  });
  private readonly staleDeco = vscode.window.createTextEditorDecorationType({
    after: {
      margin: "0 0 0 0.75em",
      color: new vscode.ThemeColor("disabledForeground"),
    },
  });
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
        if (e.document.languageId === "iec-st") this.scheduleRender();
      })
    );
    this.onEditorsChanged();
  }

  toggle(): void {
    this.enabled = !this.enabled;
    void vscode.workspace
      .getConfiguration("nautilus")
      .update("liveValues.enabled", this.enabled, vscode.ConfigurationTarget.Global);
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

  private stEditors(): vscode.TextEditor[] {
    return vscode.window.visibleTextEditors.filter(
      (e) => e.document.languageId === "iec-st"
    );
  }

  /** Connect only while enabled and an ST editor is visible. */
  private onEditorsChanged(): void {
    const wanted = this.enabled && this.stEditors().length > 0;
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
    });
    req.on("error", () => this.scheduleReconnect());
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
    this.casing.clear();
    for (const [name, value] of Object.entries(frame.tags ?? {})) {
      this.values.set(name.toLowerCase(), value);
      this.casing.set(name.toLowerCase(), name);
    }
    const wasStale = !this.fresh();
    this.lastFrameMs = Date.now();
    if (wasStale) this.updateStatus();
    this.scheduleRender();
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
      for (const site of scanIdentifiers(text, this.values)) {
        const pos = editor.document.positionAt(site.end);
        const value = this.values.get(site.lowerName);
        decos.push({
          range: new vscode.Range(pos, pos),
          renderOptions: {
            after: { contentText: ` ${formatValue(value)}` },
          },
          hoverMessage: `${this.casing.get(site.lowerName) ?? site.lowerName} — live value from ${this.runtimeUrl()}`,
        });
      }
      editor.setDecorations(fresh ? this.staleDeco : this.freshDeco, []);
      editor.setDecorations(fresh ? this.freshDeco : this.staleDeco, decos);
    }
  }

  private updateStatus(): void {
    if (this.stEditors().length === 0) {
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
    for (const d of this.disposables) d.dispose();
  }
}

