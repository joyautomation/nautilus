// nautilus IEC 61131-3 Structured Text extension.
//
// Three layers, each independent of the next:
//   1. Declarative syntax highlighting (contributes.grammars — no code).
//   2. Language intelligence: spawns `nautilus lsp` (the nautilus CLI's
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
import { OnlineEdit } from "./onlineEdit";
import { broadcastSyncState, FbdEditorProvider, FbdPreview } from "./fbdPreview";
import { LdEditorProvider, LdPreview } from "./ldPreview";
import { SfcEditorProvider, SfcPreview } from "./sfcPreview";
import { MimicEditorProvider } from "./mimicEditor";
import { ComponentEditorProvider } from "./componentEditor";
import { UserComponentManager } from "./userComponents";
import { registerEditComponentPortsCommand } from "./editComponentPorts";
import { AcceptanceTests } from "./acceptanceTests";

let client: LanguageClient | undefined;
let live: LiveValues | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // Register commands and live values FIRST, independent of the language
  // client: they don't need it, and if the CLI is missing we must not let a
  // failed/slow client start block them (otherwise the toggle command is
  // "not found" and live values never connect).
  live = new LiveValues();
  context.subscriptions.push(live);

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
  context.subscriptions.push(new LdEditorProvider(context, live).register());
  context.subscriptions.push(new SfcEditorProvider(context, live).register());
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
    vscode.commands.registerCommand("nautilus.program.download", () => online.download()),
    vscode.commands.registerCommand("nautilus.program.diff", () => online.diff()),
    vscode.commands.registerCommand("nautilus.program.rollback", () => online.rollback()),
    vscode.commands.registerCommand("nautilus.program.pull", () => online.pull()),
    vscode.commands.registerCommand("nautilus.restartLanguageServer", async () => {
      await client?.stop().catch(() => undefined);
      client = undefined;
      await startLanguageClient(context);
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nautilus.runtimeUrl") || e.affectsConfiguration("nautilus.liveValues.enabled")) {
        live?.configChanged();
      }
    })
  );

  await startLanguageClient(context);
}

async function startLanguageClient(context: vscode.ExtensionContext): Promise<void> {
  const cliPath = vscode.workspace
    .getConfiguration("nautilus")
    .get<string>("cliPath", "nautilus");

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
  } catch {
    client = undefined;
    // Syntax highlighting, commands, and live values still work without the
    // server; point the user at the one-line install instead of failing hard.
    // Fire-and-forget: do NOT await the toast — an un-dismissed notification
    // would otherwise leave activate() pending forever.
    void vscode.window
      .showWarningMessage(
        `nautilus: couldn't start the language server ("${cliPath} lsp"). ` +
          "Install the CLI for diagnostics and go-to-definition.",
        "Copy install command"
      )
      .then((pick) => {
        if (pick) {
          void vscode.env.clipboard.writeText(
            "go install github.com/joyautomation/nautilus/cmd/nautilus@latest"
          );
        }
      });
  }
}

export function deactivate(): Thenable<void> | undefined {
  live?.dispose();
  return client?.stop();
}
