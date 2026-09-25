// nautilus IEC 61131-3 Structured Text extension.
//
// Three layers, each independent of the next:
//   1. Declarative syntax highlighting (contributes.grammars — no code).
//   2. Language intelligence: spawns `naut lsp` (the nautilus CLI's
//      language-server subcommand) over stdio for compile diagnostics,
//      go-to-definition, hover, and completion.
//   3. Inline live values: subscribes to a running controller's tag API
//      (server package, /api/stream) and decorates identifiers in .st
//      files with their current runtime values — the mini-scada
//      CodeMirror inline-values idea, ported to VS Code.

import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
} from "vscode-languageclient/node";
import { FbMonitorLenses, LiveValues } from "./liveValues";
import { LiveValuesView } from "./liveValuesView";
import { OnlineEdit } from "./onlineEdit";
import { broadcastSyncState, FbdEditorProvider, FbdPreview } from "./fbdPreview";
import { LdEditorProvider, LdPreview } from "./ldPreview";
import { SfcEditorProvider, SfcPreview } from "./sfcPreview";
import { registerDiagramCommands } from "./diagramCommands";
import { MimicEditorProvider } from "./mimicEditor";
import { ComponentEditorProvider } from "./componentEditor";
import { UserComponentManager } from "./userComponents";
import { registerEditComponentPortsCommand } from "./editComponentPorts";
import { registerNewProjectCommand } from "./newProject";
import { AcceptanceTests } from "./acceptanceTests";
import { checkCliVersion, initCli, installCliCommand, resolveCliNow, showCliInfo, showCliMissing } from "./cli";

/** The id `workbench.action.openWalkthrough` wants: `<extension id>#<walkthrough id>`,
 * matching this file's `contributes.walkthroughs[0].id` in package.json. */
function walkthroughId(context: vscode.ExtensionContext): string {
  return `${context.extension.id}#gettingStarted`;
}

/** Getting Started's "Create a project" step also completes from a
 * terminal `naut new` (not just the nautilus.newProject command): whether
 * the workspace has a nautilus.yaml anywhere is exposed as a `when`-clause
 * context, live-updated as one comes or goes. */
function watchHasProject(context: vscode.ExtensionContext): void {
  const refresh = async () => {
    const found = await vscode.workspace.findFiles("**/nautilus.yaml", "**/node_modules/**", 1);
    void vscode.commands.executeCommand("setContext", "nautilus.hasProject", found.length > 0);
  };
  void refresh();
  const watcher = vscode.workspace.createFileSystemWatcher("**/nautilus.yaml");
  context.subscriptions.push(watcher, watcher.onDidCreate(refresh), watcher.onDidDelete(refresh));
}

let client: LanguageClient | undefined;
let live: LiveValues | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // Before anything resolves the CLI: the managed install's directory is
  // part of the search.
  initCli(context);

  // Register commands and live values FIRST, independent of the language
  // client: they don't need it, and if the CLI is missing we must not let a
  // failed/slow client start block them (otherwise the toggle command is
  // "not found" and live values never connect).
  live = new LiveValues();
  context.subscriptions.push(live);

  // The Live Values panel: a tree of tags/locals with a per-tag "Set value"
  // pencil, for setting several values in a row from a list.
  const liveValuesView = new LiveValuesView(live);
  context.subscriptions.push(
    liveValuesView,
    vscode.window.registerTreeDataProvider("nautilusLiveValues", liveValuesView),
    vscode.commands.registerCommand("nautilus.liveValues.refresh", () => liveValuesView.refresh())
  );

  // Acceptance tests in the Test Explorer. Independent of the language
  // client too: it shells out to the CLI, and a project's tests should be
  // runnable whether or not the server came up.
  context.subscriptions.push(new AcceptanceTests());

  // The status poll's verdict streams into every diagram webview, so
  // divergence from the live program is visible where the editing happens.
  const online = new OnlineEdit(broadcastSyncState);
  context.subscriptions.push(online);

  const fbd = new FbdPreview(context, live);
  context.subscriptions.push(fbd);

  const ladder = new LdPreview(context, live);
  context.subscriptions.push(ladder);
  const sfc = new SfcPreview(context, live);
  context.subscriptions.push(sfc);
  // "Open With → FBD Diagram": the diagram as a real editor over the .fbd
  // document (text remains the default editor).
  context.subscriptions.push(new FbdEditorProvider(context, live).register());
  const ldEditor = new LdEditorProvider(context, live);
  context.subscriptions.push(ldEditor.register());
  // Rockwell L5X exports render through the same ladder editor.
  context.subscriptions.push(ldEditor.register(LdEditorProvider.l5xViewType));
  context.subscriptions.push(new SfcEditorProvider(context, live).register());
  // Title-bar hops between the text and the diagram editor.
  context.subscriptions.push(registerDiagramCommands());
  // User-authored Svelte components rendered for real inside the mimic/
  // Component Editor webviews — gated on workspace trust (it compiles and
  // runs the project's own code via esbuild + its svelte/compiler).
  const userComponents = new UserComponentManager(context);
  context.subscriptions.push(userComponents);
  context.subscriptions.push(
    vscode.workspace.onDidGrantWorkspaceTrust(() => userComponents.activateTrust())
  );

  // "*.mimic.json" opens as the graphical HMI mimic editor by default (the
  // doc is spatial-first; the JSON is an "Open With → Text Editor" away).
  const mimic = new MimicEditorProvider(context, userComponents);
  context.subscriptions.push(mimic.register());
  context.subscriptions.push(new ComponentEditorProvider(context, userComponents).register());
  // Graphical port editing for BUILT-INS (Tank, Pump, ...), reusing the same
  // Component Editor a project's own custom components get — shares the
  // mimic editor's workspace-wide sidecar index so a sidecar created here
  // shows up in every open mimic panel immediately.
  context.subscriptions.push(registerEditComponentPortsCommand(mimic.componentIndex));
  context.subscriptions.push(registerNewProjectCommand());

  // Getting Started walkthrough: its own command (also reachable from the
  // Command Palette any time, not just on a fresh install), and the
  // "workspace already has a project" context its "Create a project" step
  // completes on.
  context.subscriptions.push(
    vscode.commands.registerCommand("nautilus.getStarted", () =>
      vscode.commands.executeCommand("workbench.action.openWalkthrough", walkthroughId(context), false)
    )
  );
  watchHasProject(context);

  // Auto-open the walkthrough at most once ever (a globalState flag, not
  // per-workspace), and only into a workspace that isn't already a nautilus
  // project — someone opening an existing project for the first time with
  // this extension doesn't need onboarding, and VS Code already offers its
  // own "open walkthrough" prompt on a fresh extension install for anyone
  // starting from nothing.
  if (!context.globalState.get<boolean>("nautilus.walkthroughShown", false)) {
    void context.globalState.update("nautilus.walkthroughShown", true);
    void vscode.workspace.findFiles("**/nautilus.yaml", "**/node_modules/**", 1).then((found) => {
      if (found.length === 0) {
        void vscode.commands.executeCommand("workbench.action.openWalkthrough", walkthroughId(context), false);
      }
    });
  }

  context.subscriptions.push(
    vscode.commands.registerCommand("nautilus.liveValues.toggle", () =>
      live?.toggle()
    ),
    // FUNCTION_BLOCK instance monitoring: the CodeLens over each block
    // header picks which called instance the body's pills read from.
    vscode.commands.registerCommand("nautilus.fb.monitor", (fbType?: string) => {
      if (fbType) void live?.pickMonitor(fbType);
    }),
    vscode.languages.registerCodeLensProvider(
      { language: "iec-st" },
      new FbMonitorLenses(live)
    ),
    vscode.commands.registerCommand("nautilus.fbd.preview", () => fbd.preview()),
    vscode.commands.registerCommand("nautilus.ld.preview", () => ladder.preview()),
    vscode.commands.registerCommand("nautilus.sfc.preview", () => sfc.preview()),
    vscode.commands.registerCommand("nautilus.fbd.diff", () => fbd.diff()),
    vscode.commands.registerCommand("nautilus.fbd.diffController", () => fbd.diffController()),
    vscode.commands.registerCommand("nautilus.ld.diff", () => ladder.diff()),
    vscode.commands.registerCommand("nautilus.ld.diffController", () => ladder.diffController()),
    vscode.commands.registerCommand("nautilus.sfc.diff", () => sfc.diff()),
    vscode.commands.registerCommand("nautilus.sfc.diffController", () => sfc.diffController()),
    vscode.commands.registerCommand("nautilus.fbd.diffRevisions", () => fbd.diffRevisions()),
    vscode.commands.registerCommand("nautilus.ld.diffRevisions", () => ladder.diffRevisions()),
    vscode.commands.registerCommand("nautilus.sfc.diffRevisions", () => sfc.diffRevisions()),
    // The connection is just the nautilus.runtimeUrl setting; this command
    // is the discoverable way to change it. The config watcher below does
    // the actual reconnect, so writing the setting IS connecting. Recently
    // used controllers are offered first — a plant floor is many of them —
    // with free-text entry for a new URL.
    vscode.commands.registerCommand("nautilus.connect", async () => {
      const cfg = vscode.workspace.getConfiguration("nautilus");
      const current = cfg.get<string>("runtimeUrl", "http://localhost:8080");
      const recent = context.globalState.get<string[]>("nautilus.recentControllers", []);
      const known = [current, ...recent.filter((u) => u !== current)];
      const items = (typed: string): vscode.QuickPickItem[] => {
        const list: vscode.QuickPickItem[] = known.map((u) => ({
          label: u,
          description: u === current ? "current" : "recent",
        }));
        // The typed value is always acceptable as-is; alwaysShow keeps it
        // visible when it matches no recent entry.
        if (typed && !known.includes(typed)) {
          list.unshift({ label: typed, description: "connect", alwaysShow: true });
        }
        return list;
      };
      const qp = vscode.window.createQuickPick();
      qp.title = "nautilus: Connect to Controller";
      qp.placeholder = "Pick a recent controller or type a base URL, e.g. http://plc-01:8080";
      qp.items = items("");
      qp.onDidChangeValue((v) => (qp.items = items(v.trim())));
      const url = await new Promise<string | undefined>((resolve) => {
        qp.onDidAccept(() => {
          const pick = qp.selectedItems[0]?.label ?? qp.value.trim();
          try {
            new URL(pick);
          } catch {
            qp.title = "Enter a full URL, e.g. http://plc-01:8080";
            return; // stay open — the title is the validation message
          }
          resolve(pick);
          qp.hide();
        });
        qp.onDidHide(() => {
          resolve(undefined); // no-op if already resolved by accept
          qp.dispose();
        });
        qp.show();
      });
      if (!url) return;
      const target = vscode.workspace.workspaceFolders
        ? vscode.ConfigurationTarget.Workspace
        : vscode.ConfigurationTarget.Global;
      await cfg.update("runtimeUrl", url, target);
      await context.globalState.update(
        "nautilus.recentControllers",
        [url, ...recent.filter((u) => u !== url)].slice(0, 8)
      );
      // Probe once for immediate feedback; the live-values stream retries
      // on its own either way, so a miss here is a warning, not a failure.
      try {
        const res = await fetch(url + "/api/state", { signal: AbortSignal.timeout(3000) });
        if (!res.ok) throw new Error(res.statusText);
        void vscode.window.showInformationMessage(`nautilus: connected — following ${url}`);
      } catch {
        void vscode.window.showWarningMessage(
          `nautilus: no controller answering at ${url} yet — live values will keep retrying`
        );
      }
    }),
    vscode.commands.registerCommand("nautilus.setValue", (tag?: string) => live?.setValue(tag)),
    vscode.commands.registerCommand("nautilus.program.download", () => online.download()),
    vscode.commands.registerCommand("nautilus.program.diff", () => online.diff()),
    vscode.commands.registerCommand("nautilus.program.rollback", () => online.rollback()),
    vscode.commands.registerCommand("nautilus.program.pull", () => online.pull()),
    vscode.commands.registerCommand("nautilus.installCli", () => installCliCommand()),
    vscode.commands.registerCommand("nautilus.showCliInfo", () => showCliInfo()),
    vscode.commands.registerCommand("nautilus.restartLanguageServer", async () => {
      await client?.stop().catch(() => undefined);
      client = undefined;
      await startLanguageClient(context);
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nautilus.runtimeUrl") || e.affectsConfiguration("nautilus.liveValues.enabled")) {
        live?.configChanged();
      }
      if (e.affectsConfiguration("nautilus.cliPath")) {
        void vscode.commands.executeCommand("nautilus.restartLanguageServer");
      }
    })
  );

  await startLanguageClient(context);
}

async function startLanguageClient(context: vscode.ExtensionContext): Promise<void> {
  const cli = resolveCliNow();
  if (!cli.found) {
    // Don't spawn a command we already know isn't there: the language
    // client would add its own error dialog and output-channel noise on top
    // of ours. Syntax highlighting, commands, and live values still work.
    showCliMissing(cli.command);
    return;
  }
  const cliPath = cli.command;
  // Logs which naut and what version, and warns if it's too old. Not
  // awaited: a slow `naut version` must not hold up the language server.
  void checkCliVersion();

  const serverOptions: ServerOptions = {
    command: cliPath,
    args: ["lsp"],
  };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [
      { language: "iec-st" },
      { language: "iec-fbd" },
      { language: "iec-ld" },
      { language: "iec-sfc" },
      // Acceptance suites: YAML holding ST expectation expressions, which
      // the server compiles and completes like any other ST. The language
      // id is "yaml" with the YAML extension installed and "plaintext"
      // without it, and both must reach the server.
      { language: "yaml", pattern: "**/*_test.yaml" },
      { language: "plaintext", pattern: "**/*_test.yaml" },
    ],
  };

  client = new LanguageClient(
    "nautilus-st",
    "nautilus Structured Text",
    serverOptions,
    clientOptions
  );

  try {
    await client.start();
    context.subscriptions.push({ dispose: () => client?.stop() });
  } catch (err) {
    client = undefined;
    // Syntax highlighting, commands, and live values still work without the
    // server. Found-but-won't-start is usually an explicit cliPath that's
    // wrong, or a CLI too old to have `lsp`.
    showCliMissing(cliPath, `"${cliPath} lsp" failed: ${err instanceof Error ? err.message : String(err)}`);
  }
}

export function deactivate(): Thenable<void> | undefined {
  live?.dispose();
  return client?.stop();
}
