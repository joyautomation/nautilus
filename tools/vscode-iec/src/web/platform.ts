// The web build's platform.ts: the extension running in a browser (the
// nautilus playground), where `naut` is the playground engine. On a real
// machine `naut run` is started in a terminal and the extension connects to
// it; here the extension starts the controller itself: Run (F5) compiles the
// project's files as saved and starts scanning, and the extension's live
// values, online edit, forces and Test Explorer reach it through the engine.
import * as vscode from "vscode";
import { engine, patchFetch, PLAYGROUND_URL } from "./engine";

export const IS_WEB = true;

type LoadResult = { ok: boolean; error?: string; notices?: string[]; ms: number };

export function registerPlatform(context: vscode.ExtensionContext, live: { configChanged(): void } | undefined): void {
  patchFetch();
  if (!engine.available()) return;

  const out = vscode.window.createOutputChannel("nautilus Playground");
  const problems = vscode.languages.createDiagnosticCollection("nautilus-run");
  const item = vscode.window.createStatusBarItem("nautilus.playground", vscode.StatusBarAlignment.Left, 1000);
  item.name = "nautilus: Run";
  context.subscriptions.push(out, problems, item);

  let running = false;
  let shownNotices = "";
  const show = () => {
    item.text = running ? "$(debug-restart) Restart" : "$(play) Run";
    item.tooltip = running
      ? "Compile the saved files and restart from the init: values (F5)"
      : "Compile the project and start scanning (F5)";
    item.command = "nautilus.playground.run";
    item.show();
  };

  async function run(): Promise<void> {
    await vscode.workspace.saveAll(false);
    const r = await engine.call<LoadResult>("run");
    problems.clear();
    const notices = r.notices ?? [];
    if (notices.length && notices.join() !== shownNotices) {
      shownNotices = notices.join();
      for (const n of notices) void vscode.window.showInformationMessage(n);
    }
    if (!r.ok) {
      const msg = r.error ?? "the project did not start";
      out.appendLine(`run: ${msg}`);
      reportCompileError(msg);
      void vscode.window
        .showErrorMessage(running ? `Not applied, the previous program is still running: ${msg}` : `The project did not start: ${msg}`, "Show Output")
        .then((pick) => pick && out.show(true));
      show();
      return;
    }
    out.appendLine(`run: compiled and started in ${Math.round(r.ms)} ms`);
    running = true;
    show();
    // Live values follow the controller; a fresh one has fresh tags.
    live?.configChanged();
  }

  /** "program.ld:4:5: message" → a diagnostic on that file. */
  function reportCompileError(msg: string): void {
    const m = msg.match(/(?:^|\s)([\w./-]+\.(?:st|ld|fbd|sfc|yaml)):(\d+)(?::(\d+))?:?\s*(.*)$/s);
    const folder = vscode.workspace.workspaceFolders?.[0]?.uri;
    if (!m || !folder) return;
    const line = Math.max(0, Number(m[2]) - 1);
    const col = Math.max(0, Number(m[3] ?? 1) - 1);
    const d = new vscode.Diagnostic(new vscode.Range(line, col, line, col + 1), m[4] || msg, vscode.DiagnosticSeverity.Error);
    d.source = "nautilus run";
    problems.set(vscode.Uri.joinPath(folder, m[1]), [d]);
  }

  context.subscriptions.push(
    vscode.commands.registerCommand("nautilus.playground.run", run),
    vscode.commands.registerCommand("nautilus.playground.stop", async () => {
      await engine.call("stop");
      running = false;
      out.appendLine("stopped");
      show();
    })
  );

  // The controller URL is the engine's: point live values there if the
  // workspace has not (the playground's defaults do).
  const cfg = vscode.workspace.getConfiguration("nautilus");
  if (cfg.get<string>("runtimeUrl") !== PLAYGROUND_URL) {
    void cfg.update("runtimeUrl", PLAYGROUND_URL, vscode.ConfigurationTarget.Global);
  }

  show();
  // A playground opens running, so the first thing anyone sees moves, and
  // with a diagram beside the program text: the extension's text-left,
  // diagram-right, as "Open Diagram to the Side" gives it on a desktop.
  const arrange = async () => {
    await openFirstDiagram();
    await reopenMimics();
  };
  void run().then(() => {
    // On a slow machine the extension can activate before the workbench
    // has restored the project's first editor: then wait for it, once.
    if (vscode.window.activeTextEditor) return arrange();
    const once = vscode.window.onDidChangeActiveTextEditor((ed) => {
      if (!ed) return;
      once.dispose();
      void arrange();
    });
    context.subscriptions.push(once);
  });
}

const DIAGRAMS: Record<string, string> = { "iec-ld": "nautilus.ldDiagram", "iec-fbd": "nautilus.fbdDiagram", "iec-sfc": "nautilus.sfcDiagram" };

async function openFirstDiagram(): Promise<void> {
  const ed = vscode.window.activeTextEditor ?? vscode.window.visibleTextEditors[0];
  const viewType = ed && DIAGRAMS[ed.document.languageId];
  if (!ed || !viewType) return;
  const open = vscode.window.tabGroups.all.some((g) =>
    g.tabs.some((t) => t.input instanceof vscode.TabInputCustom && t.input.viewType === viewType)
  );
  if (open) return;
  await vscode.commands.executeCommand("vscode.openWith", ed.document.uri, viewType, {
    viewColumn: vscode.ViewColumn.Beside,
    preserveFocus: true,
  });
}

const MIMIC = "nautilus.mimicEditor";
const isMimicText = (t: vscode.Tab): t is vscode.Tab & { input: vscode.TabInputText } =>
  t.input instanceof vscode.TabInputText && t.input.uri.path.endsWith(".mimic.json");

/** The playground's startup layout opens a project's tabs before this
 * extension has registered the mimic editor, so a mimic there comes up as
 * its JSON. Reopen each one as the HMI, keeping the tab that was showing. */
async function reopenMimics(): Promise<void> {
  for (const group of vscode.window.tabGroups.all) {
    const shown = group.activeTab?.input;
    const uris = group.tabs.filter(isMimicText).map((t) => t.input.uri);
    if (!uris.length) continue;
    for (const uri of uris) {
      await vscode.commands.executeCommand("vscode.openWith", uri, MIMIC, {
        viewColumn: group.viewColumn,
        preserveFocus: true,
        preview: false,
      });
      const text = group.tabs.find((t) => isMimicText(t) && t.input.uri.toString() === uri.toString());
      if (text) await vscode.window.tabGroups.close(text, true);
    }
    if (shown instanceof vscode.TabInputText && !shown.uri.path.endsWith(".mimic.json")) {
      await vscode.window.showTextDocument(shown.uri, { viewColumn: group.viewColumn, preserveFocus: true, preview: false });
    }
  }
}
