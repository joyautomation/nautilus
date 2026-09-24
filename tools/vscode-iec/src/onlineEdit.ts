// PLC-style online edits: push the workspace's ST program to a running
// nautilus controller (warm swap — retained state carries over), diff what
// the controller is running against the workspace, and roll back the last
// push. Talks to the runtime's program API:
//
//   GET  <runtimeUrl>/api/program           running source + hash + dirty
//   PUT  <runtimeUrl>/api/program           {source, baseHash} → swap
//   POST <runtimeUrl>/api/program/rollback  one-step stateful undo
//
// The controller must opt in (server.Options.OnlineEdits) — production
// controllers keep it off. Edits are ephemeral: a restart reverts to the
// deployed program; committing the file is what makes an edit permanent.
//
// Program composition mirrors the runtime and the language server's project
// rule (internal/stproject): sibling .st files with no PROGRAM are libraries
// and precede the program file, sorted by name. The same libraries join
// every program in a multi-program project, so a diff from a library file
// needs no task choice — it compares the shared library text against every
// task's copy (see diffLibraries).

import * as vscode from "vscode";
import {
  controllerPrelude,
  downloadConfirmMessage,
  forceDownloadConfirmMessage,
  normalize,
  pouOf,
  rollbackConfirmMessage,
  splitProgram,
} from "./programSync";

/** One entry in the GET /api/program directory — every program in the
 * resource, source omitted. */
export type ProgramSummary = {
  task?: string;
  pou?: string;
  language?: string;
  hash: string;
  dirty: boolean;
};

export type ProgramInfo = {
  task?: string; // task name on the controller ("main", or a Task's name)
  pou?: string; // `PROGRAM <Name>` — the edit-routing identity
  source: string;
  language?: "st" | "fbd" | "ld" | "sfc"; // which language the controller's program is in
  hash: string;
  dirty: boolean;
  editable: boolean;
  canRollback: boolean;
  programs?: ProgramSummary[];
  error?: string;
};

/** All four IEC languages participate in online edits: a project's program
 * file may be .st, .fbd, .ld, or .sfc (the runtime accepts and serves any
 * of them — internal/stproject includes all four). */
function isIecLang(languageId: string): boolean {
  return languageId === "iec-st" || languageId === "iec-fbd" ||
    languageId === "iec-ld" || languageId === "iec-sfc";
}

const REMOTE_SCHEME = "nautilus-controller";
const LOCAL_SCHEME = "nautilus-workspace";
const POLL_MS = 3000;

const IEC_FILE = /\.(st|fbd|ld|sfc)$/i;

/** The IEC document the user is "in": the active text editor, or — because
 * the graphical editors are custom editors that never appear in
 * activeTextEditor — the active tab's custom-editor document. */
function activeIecUri(): vscode.Uri | undefined {
  const active = vscode.window.activeTextEditor?.document;
  if (active && isIecLang(active.languageId) && active.uri.scheme === "file") return active.uri;
  const input = vscode.window.tabGroups.activeTabGroup.activeTab?.input;
  if (input instanceof vscode.TabInputCustom && IEC_FILE.test(input.uri.path)) return input.uri;
  return undefined;
}

/** Is any IEC surface open — a text editor or one of our diagram tabs? */
function iecSurfaceVisible(): boolean {
  if (vscode.window.visibleTextEditors.some((e) => isIecLang(e.document.languageId))) return true;
  return vscode.window.tabGroups.all.some((g) =>
    g.tabs.some((t) => t.input instanceof vscode.TabInputCustom && t.input.viewType.startsWith("nautilus."))
  );
}

/** How the workspace relates to the running controller — broadcast to the
 * diagram webviews so divergence is visible where the editing happens. */
export type SyncState = "sync" | "edit" | "differs" | "offline";

export class OnlineEdit implements vscode.Disposable {
  private status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 89);
  private timer: NodeJS.Timeout;
  /** Virtual diff documents, keyed by scheme+path — a library diff can open
   * one view per divergent task, so a single remote/local slot is not enough. */
  private docs = new Map<string, string>();
  private disposables: vscode.Disposable[] = [];

  constructor(private readonly onState?: (state: SyncState, programUri?: vscode.Uri) => void) {
    this.status.command = "nautilus.program.diff";
    this.timer = setInterval(() => void this.refreshStatus(), POLL_MS);

    const provider: vscode.TextDocumentContentProvider = {
      provideTextDocumentContent: (uri) => this.docs.get(uri.scheme + uri.path) ?? "",
    };
    this.disposables.push(
      vscode.workspace.registerTextDocumentContentProvider(REMOTE_SCHEME, provider),
      vscode.workspace.registerTextDocumentContentProvider(LOCAL_SCHEME, provider)
    );
    void this.refreshStatus();
  }

  private runtimeUrl(): string {
    return vscode.workspace
      .getConfiguration("nautilus")
      .get<string>("runtimeUrl", "http://localhost:8080")
      .replace(/\/+$/, "");
  }

  /** Whether download() and rollback() should ask before writing to the
   * controller (nautilus.confirmControllerWrites). Defaults on — a local
   * tunnel can point at a real plant, so there's no "safe" URL to skip it
   * for; people iterating against a sim turn this off deliberately. */
  private confirmWritesEnabled(): boolean {
    return vscode.workspace.getConfiguration("nautilus").get<boolean>("confirmControllerWrites", true);
  }

  /** Headers for a write request: JSON plus the bearer token when the
   * controller requires one (nautilus.token), so online edits reach a
   * token-gated controller on the network. */
  private writeHeaders(): Record<string, string> {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    const token = vscode.workspace.getConfiguration("nautilus").get<string>("token", "");
    if (token) headers["Authorization"] = "Bearer " + token;
    return headers;
  }

  /** Fetch a program's info. With a POU name, targets that program on a
   * multi-task controller; an unknown POU falls back to the main program
   * so single-program controllers (and renames) keep working. */
  private async fetchInfo(pou?: string): Promise<ProgramInfo | undefined> {
    try {
      if (pou) {
        const res = await fetch(this.runtimeUrl() + "/api/program?pou=" + encodeURIComponent(pou));
        if (res.ok) return (await res.json()) as ProgramInfo;
        if (res.status !== 404) return undefined;
        // 404: no program by that name — fall through to main.
      }
      const res = await fetch(this.runtimeUrl() + "/api/program");
      if (!res.ok) return undefined;
      return (await res.json()) as ProgramInfo;
    } catch {
      return undefined;
    }
  }

  /** Register a virtual document and return a URI for it — the query param
   * defeats VS Code's content cache so each diff shows fresh content. */
  private setDoc(scheme: string, path: string, content: string): vscode.Uri {
    this.docs.set(scheme + path, content);
    return vscode.Uri.parse(`${scheme}:${path}?${Date.now()}`);
  }

  /**
   * Decompose the project directory the way the runtime does
   * (stproject.ComposeAll): .st files with no PROGRAM (sorted by name) are
   * libraries shared by every program; each file with a PROGRAM — .st, .fbd,
   * .ld, or .sfc — is one program. Open editor buffers win over on-disk
   * content.
   */
  private async composeAll(): Promise<
    | {
        dir: vscode.Uri;
        prelude: string;
        activeFile: string;
        programs: { file: string; uri: vscode.Uri; body: string; pou: string }[];
        libraries: { file: string; uri: vscode.Uri }[];
      }
    | undefined
  > {
    const activeUri = activeIecUri();
    let dir: vscode.Uri | undefined;
    if (activeUri) {
      dir = vscode.Uri.joinPath(activeUri, "..");
    } else if (vscode.workspace.workspaceFolders?.length) {
      dir = vscode.workspace.workspaceFolders[0].uri;
    }
    if (!dir) return undefined;

    const entries = await vscode.workspace.fs.readDirectory(dir);
    const iecFiles = entries
      .filter(([name, kind]) => kind === vscode.FileType.File && IEC_FILE.test(name))
      .map(([name]) => name)
      .sort();

    const contents = new Map<string, string>();
    for (const name of iecFiles) {
      const uri = vscode.Uri.joinPath(dir, name);
      const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
      contents.set(name, open ? open.getText() : new TextDecoder().decode(await vscode.workspace.fs.readFile(uri)));
    }

    const isProgram = (src: string) => /^\s*PROGRAM\b/m.test(src);
    const programs: { file: string; uri: vscode.Uri; body: string; pou: string }[] = [];
    const libraries: { file: string; uri: vscode.Uri }[] = [];
    // Only .st libraries join the prelude, matching internal/stproject.
    let prelude = "";
    for (const name of iecFiles) {
      const src = contents.get(name) ?? "";
      if (isProgram(src)) {
        programs.push({ file: name, uri: vscode.Uri.joinPath(dir, name), body: src, pou: pouOf(src) });
      } else if (/\.st$/i.test(name)) {
        libraries.push({ file: name, uri: vscode.Uri.joinPath(dir, name) });
        prelude += src.endsWith("\n") ? src : src + "\n";
      }
    }
    return { dir, prelude, activeFile: activeUri ? activeUri.path.split("/").pop() ?? "" : "", programs, libraries };
  }

  /**
   * Compose the single program the active file names (or the only one) the
   * way the runtime does (stproject.Join): shared libraries, then the
   * program body. Download, pull, and rollback route to exactly one program,
   * so an ambiguous target is an error here — diff and the status poll use
   * composeAll and handle multi-program projects themselves.
   */
  private async compose(quiet = false, action = "download"): Promise<
    | { source: string; prelude: string; programFile: string; programUri: vscode.Uri; programBody: string; pou: string }
    | undefined
  > {
    const ws = await this.composeAll();
    if (!ws) return undefined;
    if (ws.programs.length === 0) {
      if (!quiet)
        void vscode.window.showErrorMessage("nautilus: no IEC file with a PROGRAM found in " + ws.dir.fsPath);
      return undefined;
    }
    let program = ws.programs[0];
    if (ws.programs.length > 1) {
      const match = ws.programs.find((p) => p.file === ws.activeFile);
      if (!match) {
        if (!quiet)
          void vscode.window.showErrorMessage(
            `nautilus: multiple program files (${ws.programs.map((p) => p.file).join(", ")}) — open the one to ${action}`
          );
        return undefined;
      }
      program = match;
    }
    return {
      source: ws.prelude + program.body,
      prelude: ws.prelude,
      programFile: program.file,
      programUri: program.uri,
      programBody: program.body,
      pou: program.pou,
    };
  }

  /** Push the composed workspace program to the controller (warm swap). */
  async download(): Promise<void> {
    const composed = await this.compose();
    if (!composed) return;
    // Target the program the active file names — on a multi-task
    // controller the PUT routes by this POU, and the baseHash must come
    // from the same program or every task download would 409.
    const info = await this.fetchInfo(composed.pou);
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    if (!info.editable) {
      void vscode.window.showErrorMessage(
        "nautilus: this controller has online edits disabled (server.Options.OnlineEdits)"
      );
      return;
    }
    if (this.confirmWritesEnabled()) {
      const pick = await vscode.window.showWarningMessage(
        downloadConfirmMessage(this.runtimeUrl(), composed.programFile, composed.pou, info.hash),
        { modal: true },
        "Download"
      );
      if (pick !== "Download") return;
    }
    try {
      const res = await fetch(this.runtimeUrl() + "/api/program", {
        method: "PUT",
        headers: this.writeHeaders(),
        body: JSON.stringify({ source: composed.source, baseHash: info.hash }),
      });
      const body = (await res.json()) as { hash?: string; resets?: string[]; error?: string };
      if (res.status === 409) {
        // The "Force download" choice below is itself an explicit
        // confirmation — no modal on top of it, but it names the target
        // (controller URL, program file/POU) just as plainly.
        const pick = await vscode.window.showWarningMessage(
          forceDownloadConfirmMessage(this.runtimeUrl(), composed.programFile, composed.pou, body.error ?? ""),
          "Force download",
          "Show diff"
        );
        if (pick === "Force download") {
          await this.put(composed.source);
        } else if (pick === "Show diff") {
          await this.diff();
        }
        return;
      }
      if (!res.ok) {
        void vscode.window.showErrorMessage("nautilus: download rejected — " + (body.error ?? res.statusText));
        return;
      }
      const resets = body.resets?.length ? ` · reset: ${body.resets.join(", ")}` : " · all state carried";
      void vscode.window.showInformationMessage(
        `nautilus: online edit live (${body.hash})${resets} — commit the file to keep it`
      );
    } catch (e) {
      void vscode.window.showErrorMessage("nautilus: download failed — " + String(e));
    }
    void this.refreshStatus();
  }

  private async put(source: string): Promise<void> {
    const res = await fetch(this.runtimeUrl() + "/api/program", {
      method: "PUT",
      headers: this.writeHeaders(),
      body: JSON.stringify({ source }),
    });
    const body = (await res.json()) as { hash?: string; error?: string };
    if (res.ok) {
      void vscode.window.showInformationMessage(`nautilus: online edit live (${body.hash})`);
    } else {
      void vscode.window.showErrorMessage("nautilus: download rejected — " + (body.error ?? res.statusText));
    }
    void this.refreshStatus();
  }

  /** Side-by-side against the controller. From a program file (or a
   * single-program project): that program's composed source vs what its
   * task runs. From a library file in a multi-program project there is no
   * single task to pick — the same libraries join every task's composition
   * — so diff the shared library text against every task instead. */
  async diff(): Promise<void> {
    const ws = await this.composeAll();
    const program =
      ws && (ws.programs.length === 1 ? ws.programs[0] : ws.programs.find((p) => p.file === ws.activeFile));
    if (ws && !program) return this.diffLibraries(ws);

    const info = await this.fetchInfo(program?.pou);
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    const title = `nautilus: controller (${info.hash}${info.dirty ? " · online edit" : ""}) ↔ workspace`;
    if (ws && program) {
      const body = splitProgram(info.source, ws.prelude);
      if (body !== undefined) {
        // The controller carries this project's libraries, so the whole
        // difference lives in the program body — diff it against the real
        // file, which stays editable like any working-tree diff.
        const ext = /\.(fbd|ld|sfc)$/i.exec(program.file)?.[1].toLowerCase() ?? "st";
        const remote = this.setDoc(REMOTE_SCHEME, `/controller.${ext}`, body);
        await vscode.commands.executeCommand("vscode.diff", remote, program.uri, title);
        return;
      }
    }
    // Libraries diverge (or there's no workspace project): fall back to the
    // full composed source, read-only on both sides. Name the virtual docs
    // by the controller's language so the diff view gets the right syntax
    // highlighting (.fbd programs diff as .fbd).
    const ext =
      info.language === "fbd" || info.language === "ld" || info.language === "sfc" ? info.language : "st";
    const remote = this.setDoc(REMOTE_SCHEME, `/controller.${ext}`, info.source);
    const local = this.setDoc(LOCAL_SCHEME, `/workspace.${ext}`, ws && program ? ws.prelude + program.body : "");
    await vscode.commands.executeCommand("vscode.diff", remote, local, title);
  }

  /** Fetch the main program's info plus every other task's, walking the
   * directory the GET response carries. */
  private async fetchAllPrograms(main: ProgramInfo): Promise<ProgramInfo[]> {
    const infos: ProgramInfo[] = [main];
    for (const s of main.programs ?? []) {
      if (!s.pou || s.pou === main.pou) continue;
      const info = await this.fetchInfo(s.pou);
      // fetchInfo falls back to main on an unknown POU — skip such duplicates.
      if (info && !infos.some((i) => i.pou === info.pou)) infos.push(info);
    }
    return infos;
  }

  /** Diff the shared library text against every task on the controller.
   * Deployed, every task's composed source carries the same prelude, so no
   * task choice is needed. An online edit swaps one task's whole source, so
   * copies can diverge — that divergence is the finding, surfaced as one
   * diff per distinct copy rather than hidden behind a "pick one" prompt. */
  private async diffLibraries(ws: {
    prelude: string;
    programs: { pou: string; body: string }[];
    libraries: { file: string; uri: vscode.Uri }[];
  }): Promise<void> {
    const main = await this.fetchInfo();
    if (!main) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }
    const infos = await this.fetchAllPrograms(main);
    const bodies = new Map(ws.programs.map((p) => [p.pou, p.body]));
    // Group tasks by what their composed source says the libraries are.
    const groups = new Map<string, { prelude: string; tasks: string[] }>();
    for (const info of infos) {
      const prelude = controllerPrelude(info.source, ws.prelude, bodies.get(info.pou ?? ""));
      const key = normalize(prelude);
      const g = groups.get(key) ?? { prelude, tasks: [] };
      g.tasks.push(info.task ?? info.pou ?? "?");
      groups.set(key, g);
    }
    if (groups.size > 1) {
      void vscode.window.showWarningMessage(
        "nautilus: controller tasks disagree about the shared libraries — an online edit changed one task's copy: " +
          [...groups.values()].map((g) => g.tasks.join("+")).join(" vs ")
      );
    }
    let n = 0;
    for (const g of groups.values()) {
      const tasks = groups.size > 1 ? ` (${g.tasks.join(", ")})` : "";
      const remote = this.setDoc(REMOTE_SCHEME, `/controller-libraries-${n}.st`, g.prelude);
      // With a single library file the workspace side IS that file — use it
      // directly so the diff stays editable, like any working-tree diff.
      // Several library files compose into one prelude, so that side can
      // only be shown read-only.
      const local =
        ws.libraries.length === 1
          ? ws.libraries[0].uri
          : this.setDoc(LOCAL_SCHEME, `/workspace-libraries-${n}.st`, ws.prelude);
      n++;
      await vscode.commands.executeCommand(
        "vscode.diff",
        remote,
        local,
        `nautilus: controller libraries${tasks} ↔ workspace`
      );
    }
  }

  /**
   * Pull the controller's running program into the workspace program file —
   * the inverse of download, so a field online-edit can be reviewed and
   * committed. Only the program file is rewritten; generated type files are
   * never touched. Shows the change and asks before saving.
   */
  async pull(): Promise<void> {
    const composed = await this.compose(false, "pull");
    if (!composed) return;
    const info = await this.fetchInfo(composed.pou);
    if (!info) {
      void vscode.window.showErrorMessage(`nautilus: no controller at ${this.runtimeUrl()}`);
      return;
    }

    const program = splitProgram(info.source, composed.prelude);
    if (program === undefined) {
      void vscode.window.showErrorMessage(
        "nautilus: the controller's type/library sources differ from this project — " +
          "re-run `naut eip import` to reconcile the generated types before pulling the program."
      );
      return;
    }
    if (program === composed.programBody) {
      void vscode.window.showInformationMessage(`nautilus: ${composed.programFile} already matches the controller`);
      return;
    }

    // Preview the incoming change before writing.
    const pullExt = /\.(fbd|ld|sfc)$/i.exec(composed.programFile)?.[1].toLowerCase() ?? "st";
    const remote = this.setDoc(REMOTE_SCHEME, `/incoming.${pullExt}`, program);
    const local = this.setDoc(LOCAL_SCHEME, `/current.${pullExt}`, composed.programBody);
    await vscode.commands.executeCommand(
      "vscode.diff",
      local,
      remote,
      `nautilus: ${composed.programFile} (workspace ↔ controller ${info.hash})`
    );
    const pick = await vscode.window.showWarningMessage(
      `Overwrite ${composed.programFile} with the controller's program?`,
      { modal: true },
      "Pull and overwrite"
    );
    if (pick !== "Pull and overwrite") return;

    await vscode.workspace.fs.writeFile(composed.programUri, new TextEncoder().encode(program));
    void vscode.window.showInformationMessage(
      `nautilus: pulled ${composed.programFile} from controller — review the diff and commit to keep it`
    );
    void this.refreshStatus();
  }

  /** One-step stateful undo of the last download — of the program the
   * active file names, on a multi-task controller. */
  async rollback(): Promise<void> {
    const ws = await this.composeAll();
    const program =
      ws && (ws.programs.length === 1 ? ws.programs[0] : ws.programs.find((p) => p.file === ws.activeFile));
    if (ws && ws.programs.length > 1 && !program) {
      // Rolling back is per-task — falling through to main would undo the
      // wrong program.
      void vscode.window.showErrorMessage(
        `nautilus: multiple program files (${ws.programs.map((p) => p.file).join(", ")}) — open the one to roll back`
      );
      return;
    }
    if (this.confirmWritesEnabled()) {
      const pick = await vscode.window.showWarningMessage(
        rollbackConfirmMessage(this.runtimeUrl(), program?.pou ?? ""),
        { modal: true },
        "Roll back"
      );
      if (pick !== "Roll back") return;
    }
    const query = program?.pou ? "?pou=" + encodeURIComponent(program.pou) : "";
    try {
      const res = await fetch(this.runtimeUrl() + "/api/program/rollback" + query, { method: "POST", headers: this.writeHeaders() });
      const body = (await res.json()) as { hash?: string; error?: string };
      if (res.ok) {
        void vscode.window.showInformationMessage(`nautilus: rolled back to ${body.hash}`);
      } else {
        void vscode.window.showWarningMessage("nautilus: rollback — " + (body.error ?? res.statusText));
      }
    } catch (e) {
      void vscode.window.showErrorMessage("nautilus: rollback failed — " + String(e));
    }
    void this.refreshStatus();
  }

  // ── sync status ─────────────────────────────────────────────────────────

  private async refreshStatus(): Promise<void> {
    if (!iecSurfaceVisible()) {
      this.status.hide();
      return;
    }
    const ws = await this.composeAll();
    const program =
      ws && (ws.programs.length === 1 ? ws.programs[0] : ws.programs.find((p) => p.file === ws.activeFile));
    const info = await this.fetchInfo(program?.pou);
    if (!info) {
      this.status.hide();
      this.onState?.("offline", program?.uri);
      return;
    }
    let inSync = false;
    let dirty = info.dirty;
    if (ws && program) {
      inSync = normalize(ws.prelude + program.body) === normalize(info.source);
    } else if (ws && ws.programs.length > 1) {
      // A library file is active in a multi-program project: there is no
      // single program to compare, so the workspace is in sync when every
      // task runs its own composition — and every workspace program has a
      // task running it.
      const infos = await this.fetchAllPrograms(info);
      const bodies = new Map(ws.programs.map((p) => [p.pou, p.body]));
      inSync =
        infos.every((i) => {
          const body = bodies.get(i.pou ?? "");
          return body !== undefined && normalize(ws.prelude + body) === normalize(i.source);
        }) && ws.programs.every((p) => infos.some((i) => i.pou === p.pou));
      dirty = infos.some((i) => i.dirty);
    }
    if (inSync && !dirty) {
      this.status.hide(); // running exactly what was deployed — nothing to say
      this.onState?.("sync", program?.uri);
      return;
    }
    if (inSync && dirty) {
      this.status.text = "$(edit) nautilus: online edit active";
      this.status.tooltip =
        "The controller runs your latest download (matches the workspace) but not what it booted with.\n" +
        "Commit the file to keep it — a controller restart reverts. Click to diff.";
      this.onState?.("edit", program?.uri);
    } else {
      this.status.text = "$(cloud-upload) nautilus: program differs";
      this.status.tooltip =
        "The controller is running a different program than the workspace. Click to diff, " +
        "then Download Program to Controller to push.";
      this.onState?.("differs", program?.uri);
    }
    this.status.show();
  }

  dispose(): void {
    clearInterval(this.timer);
    this.status.dispose();
    for (const d of this.disposables) d.dispose();
  }
}
