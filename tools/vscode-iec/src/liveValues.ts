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
import { mirrorStatus, notifyError, notifyInfo, notifyWarning } from "./testHooks";
import { STATUS_FORCES, STATUS_LIVE, testState } from "./testState";
import {
  clearForcesConfirmMessage,
  controllerWrite,
  forceApi,
  forceConfirmMessage,
  forcedAddress,
  forcedPillText,
  forceStatusText,
  lowerForces,
  parseForces,
} from "./forces";
import { fbMonitorTitle } from "./fbMonitorTitle";
import { projectDirFor, projectFiles } from "./projectFiles";
import {
  formatValue,
  formatValueHover,
  instanceScope,
  parseTypedWrite,
  typedWriteHint,
  scanFbRegions,
  scanIdentifiers,
  scanInstanceDecls,
} from "./scan";
import {
  enumPickItems,
  enumText,
  enumValueName,
  flattenMetaTypes,
  isEnum,
  typedEnumPick,
  typeFor,
  typeLabel,
  type EnumMember,
  type FlatType,
  type FlatTypes,
} from "./tagTypes";

type Frame = {
  ts: number;
  scans: number;
  tags: Record<string, unknown>;
  // Retained program locals (a PI integral, latches, FB instances with
  // their pins) — the watch inside the POU, streamed alongside the tags.
  locals?: Record<string, unknown>;
  // The force table (address → forced value), present while any force is
  // active — see server/force.go.
  forces?: Record<string, unknown>;
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
  // Lowercased forced address → forced value — the diagrams' F badge.
  forced: Record<string, unknown>;
  // Declared types by tagTypes.typeKey (GET /api/meta): which values are
  // enumerations, and their members (#246). Sent whole with every frame —
  // small, and the stream itself stays type-free.
  types: FlatTypes;
}) => void;

export class LiveValues implements vscode.Disposable {
  private enabled: boolean;
  private values = new Map<string, unknown>(); // lowercased tag name → value
  // Last frame's tags and locals with their DECLARED casing, for the Live
  // Values panel (the decoration path lowercases; a list wants real names).
  private lastTags: [string, unknown][] = [];
  private lastLocals: [string, unknown][] = [];
  // The controller's force table as of the last frame (declared casing).
  private forces = new Map<string, unknown>();
  // Declared types from GET /api/meta (#246): fetched on every (re)connect,
  // and again when the frame's set of names changes (an online edit can
  // add an enumerated local). metaNames is the name count it was read at.
  private types: FlatTypes = {};
  private metaNames = -1;
  private metaAt = 0;
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
  // A forced value's pill: amber, with an F badge in its text — Logix and
  // TIA both mark a forced value so it can't be mistaken for the field's.
  // Literal ambers, not a ThemeColor: charts.orange rendered near-black in
  // some dark themes (seen on the rig), and a forced value must never be
  // the hard one to read. A light theme gets a darker amber for contrast.
  private readonly forcedDeco = forcedPillDecoration();
  // An enumerated value's pill (#246): the member's name, bare, in blue
  // italics — so `Run` reads as a named value, never as text, and a STRING
  // keeps its quotes in the ordinary green pill.
  private readonly enumDeco = enumPillDecoration();
  private readonly status = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Right,
    90
  );
  // "N forces active" — Logix keeps forces in plain sight, and so do we.
  private readonly forceStatus = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Right,
    89
  );

  constructor() {
    this.enabled = this.configEnabled();
    this.status.name = STATUS_LIVE;
    this.status.command = "nautilus.liveValues.toggle";
    this.forceStatus.name = STATUS_FORCES;
    this.forceStatus.command = "nautilus.forces.show";
    this.forceStatus.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
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

  /** The declared type of a tag, local or member path, from /api/meta;
   * undefined when the controller did not say (or predates #246). */
  typeOf(path: string): FlatType | undefined {
    return typeFor(this.types, path);
  }

  /** A value as every live surface shows it: an enumeration's member by
   * name, bare; anything else as formatValue renders it. */
  display(v: unknown, path: string): string {
    return enumText(v, this.typeOf(path)) ?? formatValue(v);
  }

  /** The member pick an enumerated tag's Set Live Value / Force… offers
   * instead of a free-text box: the members (current marked), and whatever
   * is typed — a member's name, `Mode#Run`, or an integer — as its own row.
   * Resolves to the integer to write (the API takes an enumerated tag's
   * value by its integer), or undefined on cancel. */
  private pickEnum(title: string, placeholder: string, ft: FlatType & { e: EnumMember[] }, current: unknown): Promise<number | undefined> {
    type Item = vscode.QuickPickItem & { value: number };
    const qp = vscode.window.createQuickPick<Item>();
    qp.title = title;
    qp.placeholder = placeholder;
    qp.matchOnDescription = false;
    const base: Item[] = enumPickItems(ft, current).map((p) => ({ label: p.label, description: p.description, value: p.value }));
    qp.items = base;
    const cur = base.find((i) => i.description?.endsWith("· current"));
    if (cur) qp.activeItems = [cur];
    return new Promise((resolve) => {
      let done = false;
      const finish = (v: number | undefined) => {
        if (done) return;
        done = true;
        resolve(v);
        qp.hide();
      };
      qp.onDidChangeValue((raw) => {
        const typed = typedEnumPick(raw, ft);
        qp.items = typed ? [{ label: typed.label, description: typed.description, value: typed.value, alwaysShow: true }, ...base] : base;
      });
      qp.onDidAccept(() => {
        const pick = qp.selectedItems[0] ?? qp.activeItems[0];
        if (pick) finish(pick.value);
      });
      qp.onDidHide(() => {
        finish(undefined);
        qp.dispose();
      });
      qp.show();
    });
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
      void notifyWarning("nautilus: put the cursor on a tag, then Set Live Value");
      return;
    }
    const current = this.valueFor(name);
    const ft = this.typeOf(name);
    if (isEnum(ft)) {
      const v = await this.pickEnum(
        `nautilus: Set ${name}`,
        `${typeLabel(ft)}${current === undefined ? "" : ` — now ${this.display(current, name)}`}: pick a member, or type one or its integer`,
        ft,
        current
      );
      if (v === undefined) return;
      await this.postValue(name, v, enumValueName(v, ft));
      return;
    }
    const prefill = current === undefined ? "" : formatValue(current);
    const hint = typedWriteHint(ft);
    const input = await vscode.window.showInputBox({
      title: `nautilus: Set ${name}`,
      prompt: current === undefined ? `New value (${hint})` : `Current ${formatValue(current)} — new value (${hint})`,
      value: prefill,
      validateInput: (v) => (parseTypedWrite(v, ft) === undefined ? `Enter ${hint}` : undefined),
    });
    if (input === undefined) return;
    const value = parseTypedWrite(input, ft);
    if (value === undefined) return;
    await this.postValue(name, value, formatValue(value));
  }

  /** POST /api/tags, and say how it went. */
  private async postValue(name: string, value: number | boolean | string, shown: string): Promise<void> {
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
        void notifyInfo(`nautilus: set ${name} = ${shown}`);
        return;
      }
      const body = await res.text();
      void notifyError(`nautilus: set ${name} rejected — ${body.trim() || res.statusText}`);
    } catch (e) {
      void notifyError(`nautilus: could not reach ${this.runtimeUrl()} — ${String(e)}`);
    }
  }

  // ── forcing ───────────────────────────────────────────────────────────

  private confirmWrites(): boolean {
    return vscode.workspace.getConfiguration("nautilus").get<boolean>("confirmControllerWrites", true);
  }

  private token(): string {
    return vscode.workspace.getConfiguration("nautilus").get<string>("token", "");
  }

  /** One controller write; reports a refusal or an unreachable controller
   * and returns whether it landed. */
  private async write(what: string, req: { method: "POST" | "DELETE"; path: string; body?: unknown }): Promise<boolean> {
    try {
      const res = await controllerWrite(this.runtimeUrl(), this.token(), req.method, req.path, req.body);
      if (res.ok) return true;
      void notifyError(`nautilus: ${what} rejected — ${res.message}`);
    } catch (e) {
      void notifyError(`nautilus: could not reach ${this.runtimeUrl()} — ${String(e)}`);
    }
    return false;
  }

  /** "Force…": hold a tag (or a struct member, by dotted path) at a value
   * until the force is removed — the controller re-applies it every scan
   * against the driver and the logic. Same entry points as Set Live Value:
   * a hover link (name), a Live Values row ({tag}), or the cursor. */
  async force(arg?: string | { tag?: string; name?: string }): Promise<void> {
    const tag = typeof arg === "string" ? arg : arg?.tag ?? arg?.name;
    const name = tag ?? this.identifierAtCursor(true);
    if (!name) {
      void notifyWarning("nautilus: put the cursor on a tag, then Force…");
      return;
    }
    const forcedAt = this.forcedFor(name);
    const current = forcedAt === name ? this.forces.get(name) : this.pathValue(name);
    const ft = this.typeOf(name);
    if (isEnum(ft)) {
      const v = await this.pickEnum(
        `nautilus: Force ${name}`,
        `${typeLabel(ft)}${current === undefined ? "" : ` — ${forcedAt === name ? "forced to" : "now"} ${this.display(current, name)}`}: force to a member (or its integer), held until you remove the force`,
        ft,
        current
      );
      if (v === undefined) return;
      await this.applyForce(name, v, enumValueName(v, ft), current === undefined ? undefined : this.display(current, name));
      return;
    }
    const hint = typedWriteHint(ft);
    const input = await vscode.window.showInputBox({
      title: `nautilus: Force ${name}`,
      prompt:
        (forcedAt === name ? `Forced to ${formatValue(current)} — new forced value` : current === undefined ? "Force to" : `Now ${formatValue(current)} — force to`) +
        ` (${hint}). Held until you remove the force.`,
      value: current === undefined ? "" : formatValue(current),
      validateInput: (v) => (parseTypedWrite(v, ft) === undefined ? `Enter ${hint}` : undefined),
    });
    if (input === undefined) return;
    const value = parseTypedWrite(input, ft);
    if (value === undefined) return;
    await this.applyForce(name, value, formatValue(value), current === undefined ? undefined : formatValue(current));
  }

  /** Confirm (when configured) and send one force. */
  private async applyForce(name: string, value: number | boolean | string, shown: string, was: string | undefined): Promise<void> {
    if (this.confirmWrites()) {
      const go = await notifyWarning(forceConfirmMessage(this.runtimeUrl(), name, shown, was), { modal: true }, "Force");
      if (go !== "Force") return;
    }
    if (await this.write(`force ${name}`, forceApi.force(name, value))) {
      void notifyInfo(`nautilus: forced ${name} = ${shown}`);
    }
  }

  /** "Remove Force": one address. With no argument, the forced address
   * under the cursor — or a pick from the table when there is none. */
  async unforce(arg?: string | { tag?: string; name?: string; force?: string }): Promise<void> {
    let name = typeof arg === "string" ? arg : arg?.force ?? arg?.tag ?? arg?.name;
    if (name) name = this.forcedFor(name) ?? name;
    if (!name) {
      const here = this.identifierAtCursor(true);
      name = here ? this.forcedFor(here) : undefined;
    }
    if (!name) {
      if (this.forces.size === 0) {
        void notifyInfo("nautilus: nothing is forced");
        return;
      }
      const pick = await vscode.window.showQuickPick(
        [...this.forces].map(([n, v]) => ({ label: n, description: `F ${this.display(v, n)}` })),
        { title: "nautilus: Remove which force?" }
      );
      if (!pick) return;
      name = pick.label;
    }
    if (await this.write(`remove force ${name}`, forceApi.unforce(name))) {
      void notifyInfo(`nautilus: removed the force on ${name}`);
    }
  }

  /** "Remove All Forces". */
  async unforceAll(): Promise<void> {
    const n = this.forces.size;
    if (this.confirmWrites()) {
      const go = await notifyWarning(clearForcesConfirmMessage(this.runtimeUrl(), n), { modal: true }, "Remove All");
      if (go !== "Remove All") return;
    }
    if (await this.write("remove all forces", forceApi.clear())) {
      void notifyInfo(`nautilus: removed all forces`);
    }
  }

  /** The status bar's target: the force table as a pick list, each entry
   * removable, plus "Remove all". */
  async showForces(): Promise<void> {
    if (this.forces.size === 0) {
      void notifyInfo("nautilus: nothing is forced");
      return;
    }
    type Item = vscode.QuickPickItem & { force?: string; all?: boolean };
    const items: Item[] = [...this.forces].map(([n, v]) => ({
      label: `$(lock) ${n}`,
      description: `F ${this.display(v, n)}`,
      detail: "Select to remove this force",
      force: n,
    }));
    items.push({ label: "$(unlock) Remove all forces", all: true });
    const pick = await vscode.window.showQuickPick(items, {
      title: `nautilus: ${this.forces.size} force${this.forces.size === 1 ? "" : "s"} active on ${this.runtimeUrl()}`,
    });
    if (!pick) return;
    if (pick.all) await this.unforceAll();
    else if (pick.force) await this.unforce(pick.force);
  }

  /** Jump a running SFC chart to a step, once (Codesys "set step"). */
  async sfcSetStep(step: string, pou?: string): Promise<boolean> {
    if (!(await this.write(`set step ${step}`, forceApi.setStep(step, pou)))) return false;
    void notifyInfo(`nautilus: chart jumped to step ${step}`);
    return true;
  }

  /** Fire one SFC transition, once. */
  async sfcFireTransition(id: string, pou?: string): Promise<boolean> {
    if (!(await this.write(`fire transition ${id}`, forceApi.fireTransition(id, pou)))) return false;
    void notifyInfo(`nautilus: fired transition ${id}`);
    return true;
  }

  /** A tag or member path's live value (case-insensitive), for prefills. */
  private pathValue(path: string): unknown {
    const [head, ...rest] = path.split(".");
    let v: unknown = this.values.get(head.toLowerCase());
    for (const seg of rest) {
      if (v === null || typeof v !== "object") return undefined;
      const obj = v as Record<string, unknown>;
      const k = Object.keys(obj).find((x) => x.toLowerCase() === seg.toLowerCase());
      v = k === undefined ? undefined : obj[k];
    }
    return v;
  }

  /** The bare identifier under the active editor's cursor, or "". Only the
   * top-level watch is offered a value write, so an FB member path (a name
   * with a dot) resolves to "" here. */
  private identifierAtCursor(dotted = false): string {
    const editor = vscode.window.activeTextEditor;
    if (!editor || !isIecDoc(editor.document)) return "";
    // A force may address a struct member ("P101.Speed"); a value write may not.
    const word = dotted ? /[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*/ : /[A-Za-z_][A-Za-z0-9_]*/;
    const range = editor.document.getWordRangeAtPosition(editor.selection.active, word);
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
      forced: this.enabled ? lowerForces(this.forces) : {},
      types: this.types,
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
      void this.fetchMeta();
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

  /** GET /api/meta → the declared types (#246). A failure keeps what we
   * had: the values still render, enumerations just read as before. */
  private async fetchMeta(): Promise<void> {
    this.metaAt = Date.now();
    try {
      const res = await fetch(this.runtimeUrl() + "/api/meta", { signal: AbortSignal.timeout(3000) });
      if (!res.ok) return;
      this.types = flattenMetaTypes(await res.json());
      this.metaNames = this.lastTags.length + this.lastLocals.length;
      this.scheduleRender();
      this.valuesChanged.fire();
    } catch {
      // controller gone or too old to answer — keep the last types
    }
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
    // A name appeared or went (an online edit, a restart on a new program):
    // the types may have too. At most every few seconds.
    if (this.lastTags.length + this.lastLocals.length !== this.metaNames && Date.now() - this.metaAt > 5000) {
      void this.fetchMeta();
    }
    const hadForces = this.forces.size;
    this.forces = parseForces(frame);
    if (hadForces !== this.forces.size) this.updateStatus();
    const wasStale = !this.fresh();
    this.lastFrameMs = Date.now();
    if (wasStale) this.updateStatus();
    this.scheduleRender();
    this.valuesChanged.fire();
  }

  /** The current tags and locals with declared casing, for the Live Values
   * panel. tags are settable (POST /api/tags by name); locals are the
   * program's retained internals and are read-only here. */
  snapshot(): {
    tags: [string, unknown][];
    locals: [string, unknown][];
    forces: ReadonlyMap<string, unknown>;
    enabled: boolean;
    fresh: boolean;
  } {
    return { tags: this.lastTags, locals: this.lastLocals, forces: this.forces, enabled: this.enabled, fresh: this.fresh() };
  }

  /** The forced address covering this tag or member path, if any. */
  forcedFor(name: string): string | undefined {
    return forcedAddress(this.forces, name);
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
        editor.setDecorations(this.forcedDeco, []);
        editor.setDecorations(this.enumDeco, []);
        continue;
      }
      const decos: vscode.DecorationOptions[] = [];
      const forcedDecos: vscode.DecorationOptions[] = [];
      const enumDecos: vscode.DecorationOptions[] = [];
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
          const full = seg.prefix + site.path;
          const ft = this.typeOf(full);
          const shown = enumText(site.value, ft) ?? formatValue(site.value);
          const hover = new vscode.MarkdownString();
          const tl = typeLabel(ft);
          hover.appendMarkdown(`**${full}**${tl ? ` · \`${tl}\`` : ""} — live value from ${this.runtimeUrl()}\n`);
          hover.appendCodeblock(formatValueHover(site.value, (v, p) => enumText(v, this.typeOf(full + p))), "");
          if (isEnum(ft)) {
            hover.appendMarkdown(`\n\n${ft.e.map((m) => (m.name === shown ? `**${m.name}**` : m.name)).join(" · ")}`);
          }
          // "Set value…" — only for a bare top-level tag (no FB-instance
          // prefix, no member/index path); a struct member isn't a writable
          // name on its own. The command link needs a trusted hover.
          const forced = seg.prefix === "" ? this.forcedFor(site.path) : undefined;
          if (forced) {
            hover.appendMarkdown(`\n\n**F** — forced to \`${this.display(this.forces.get(forced), forced)}\` (\`${forced}\`)`);
          }
          if (seg.prefix === "" && /^[A-Za-z_][A-Za-z0-9_.]*$/.test(site.path)) {
            const arg = encodeURIComponent(JSON.stringify([site.path]));
            const links: string[] = [];
            if (!forced && !site.path.includes(".")) links.push(`[$(edit) Set value…](command:nautilus.setValue?${arg})`);
            links.push(`[$(lock) Force…](command:nautilus.force?${arg})`);
            if (forced) {
              const farg = encodeURIComponent(JSON.stringify([forced]));
              links.push(`[$(unlock) Remove force](command:nautilus.unforce?${farg})`);
            }
            hover.appendMarkdown("\n\n" + links.join(" · "));
            hover.isTrusted = { enabledCommands: ["nautilus.setValue", "nautilus.force", "nautilus.unforce"] };
            hover.supportThemeIcons = true;
          }
          (forced ? forcedDecos : enumText(site.value, ft) !== undefined ? enumDecos : decos).push({
            range: new vscode.Range(pos, pos),
            renderOptions: {
              after: { contentText: forced ? forcedPillText(shown) : shown },
            },
            hoverMessage: hover,
          });
        }
      }
      // A stale frame can't vouch for a force any more than for a value:
      // forced pills grey out with the rest (the F in their text stays).
      // An enumerated pill greys out with the rest when stale; its bare
      // member name still says it is not a STRING.
      editor.setDecorations(fresh ? this.staleDeco : this.freshDeco, []);
      editor.setDecorations(fresh ? this.freshDeco : this.staleDeco, fresh ? decos : decos.concat(forcedDecos, enumDecos));
      editor.setDecorations(this.forcedDeco, fresh ? forcedDecos : []);
      editor.setDecorations(this.enumDeco, fresh ? enumDecos : []);
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
        // .ld too: a ladder program declares its block instances in the
        // same VAR sections (permissives.ld's MotorStarter instances).
        for (const { uri } of await projectFiles(root, /\.(st|fbd|ld)$/i)) {
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
      void notifyInfo(
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

  /** The force count in the status bar, and the context keys the menus
   * gate on. Shown only while frames are fresh: an offline controller's
   * force table is unknown, and a stale count would be worse than none. */
  private updateForceStatus(): void {
    const live = this.enabled && this.fresh();
    const n = live ? this.forces.size : 0;
    void vscode.commands.executeCommand("setContext", "nautilus.liveConnected", live);
    void vscode.commands.executeCommand("setContext", "nautilus.forcesActive", n > 0);
    if (n === 0) {
      this.forceStatus.hide();
      mirrorStatus(this.forceStatus, STATUS_FORCES, false);
      return;
    }
    this.forceStatus.text = forceStatusText(n);
    this.forceStatus.tooltip = `${n} forced on ${this.runtimeUrl()}: ${[...this.forces.keys()].join(", ")} — click to list or remove`;
    this.forceStatus.show();
    mirrorStatus(this.forceStatus, STATUS_FORCES, true);
  }

  private updateStatus(): void {
    this.updateForceStatus();
    // Visible while anything shows live values — a text editor OR a diagram
    // webview (the diagram's toolbar toggle drives the same command).
    if (this.stEditors().length === 0 && this.listeners.size === 0) {
      this.status.hide();
      mirrorStatus(this.status, STATUS_LIVE, false);
      testState()?.update({ connected: this.enabled && this.fresh(), liveValuesEnabled: this.enabled });
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
    mirrorStatus(this.status, STATUS_LIVE, true);
    testState()?.update({ connected: this.enabled && this.fresh(), liveValuesEnabled: this.enabled });
  }

  dispose(): void {
    this.disconnect();
    clearInterval(this.staleTimer);
    if (this.renderTimer) clearTimeout(this.renderTimer);
    this.freshDeco.dispose();
    this.staleDeco.dispose();
    this.forcedDeco.dispose();
    this.enumDeco.dispose();
    this.status.dispose();
    this.forceStatus.dispose();
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
      const title = fbMonitorTitle(inst, this.live.candidatesFor(r.type));
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

// forcedPillDecoration is pillDecoration in amber, with a darker ink on light
// themes — the forced value's pill (its text carries the F).
function forcedPillDecoration(): vscode.TextEditorDecorationType {
  const shape = "none; border-radius: 5px; padding: 0px 5px; font-size: 0.85em; vertical-align: baseline;";
  return vscode.window.createTextEditorDecorationType({
    after: { margin: "0 0 0 0.6em", fontWeight: "700", textDecoration: shape },
    dark: {
      after: { color: "#f2b13c", backgroundColor: "rgba(242, 177, 60, 0.16)", border: "1px solid rgba(242, 177, 60, 0.7)" },
    },
    light: {
      after: { color: "#9a5b00", backgroundColor: "rgba(214, 140, 20, 0.14)", border: "1px solid rgba(170, 100, 0, 0.6)" },
    },
  });
}

// enumPillDecoration is pillDecoration for an enumerated value (#246): the
// same shape (the smoke checks find pills by their 5px radius), in the
// theme's enum-member blue and italic, so a named value is never mistaken
// for a STRING's text.
function enumPillDecoration(): vscode.TextEditorDecorationType {
  return vscode.window.createTextEditorDecorationType({
    after: {
      margin: "0 0 0 0.6em",
      color: new vscode.ThemeColor("symbolIcon.enumeratorMemberForeground"),
      backgroundColor: "rgba(75, 156, 230, 0.13)",
      border: "1px solid rgba(75, 156, 230, 0.45)",
      fontWeight: "600",
      fontStyle: "italic",
      textDecoration:
        "none; border-radius: 5px; padding: 0px 5px; font-size: 0.85em; vertical-align: baseline;",
    },
  });
}
