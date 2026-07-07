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
  await startLanguageClient(context);

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
    // Syntax highlighting still works without the server; point the user
    // at the one-line install instead of failing hard.
    const pick = await vscode.window.showWarningMessage(
      `nautilus: couldn't start the language server ("${cliPath} lsp"). ` +
        "Install the CLI for diagnostics and go-to-definition.",
      "Copy install command"
    );
    if (pick) {
      await vscode.env.clipboard.writeText(
        "go install github.com/joyautomation/nautilus/cmd/nautilus@latest"
      );
    }
  }
}

export function deactivate(): Thenable<void> | undefined {
  live?.dispose();
  return client?.stop();
}
