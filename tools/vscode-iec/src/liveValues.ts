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
import { formatValue, formatValueHover, scanIdentifiers } from "./scan";

type Frame = { ts: number; scans: number; tags: Record<string, unknown> };

/** A frame is "fresh" if it arrived within this window; otherwise chips gray out. */
const FRESHNESS_MS = 3000;
const RECONNECT_MS = 2000;
const RENDER_THROTTLE_MS = 150;

export class LiveValues implements vscode.Disposable {
  private enabled: boolean;
  private values = new Map<string, unknown>(); // lowercased tag name → value
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
        if (e.document.languageId === "iec-st") this.scheduleRender();
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
    for (const [name, value] of Object.entries(frame.tags ?? {})) {
      this.values.set(name.toLowerCase(), value);
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
        // site.value is resolved down the accessor path — a member reference
        // (RTU.VALUE) shows the child value, not the parent struct.
        const hover = new vscode.MarkdownString();
        hover.appendMarkdown(`**${site.path}** — live value from ${this.runtimeUrl()}\n`);
        hover.appendCodeblock(formatValueHover(site.value), "");
        decos.push({
          range: new vscode.Range(pos, pos),
          renderOptions: {
            after: { contentText: formatValue(site.value) },
          },
          hoverMessage: hover,
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

