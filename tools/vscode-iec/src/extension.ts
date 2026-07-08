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
import { LiveValues } from "./liveValues";

let client: LanguageClient | undefined;
let live: LiveValues | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // Register commands and live values FIRST, independent of the language
  // client: they don't need it, and if the CLI is missing we must not let a
  // failed/slow client start block them (otherwise the toggle command is
  // "not found" and live values never connect).
  live = new LiveValues();
  context.subscriptions.push(live);

  context.subscriptions.push(
    vscode.commands.registerCommand("nautilus.liveValues.toggle", () =>
      live?.toggle()
    ),
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
    documentSelector: [{ language: "iec-st" }],
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
