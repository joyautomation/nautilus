// nautilus IEC 61131-3 Structured Text extension.
//
// Today this extension is purely declarative: the syntax highlighting is
// provided entirely by `contributes.grammars` / `contributes.languages` in
// package.json, so no activation is required and this file is not wired in
// (package.json has `main` and `activationEvents` commented out).
//
// This scaffold is the entry point for the next phase: launching the nautilus
// `lang/st` compiler as a language-server binary and connecting a
// vscode-languageclient over stdio. When that lands, uncomment `main` and
// `activationEvents` in package.json, compile with `npm run compile`, and this
// `activate` will start the client.

import * as vscode from "vscode";

export function activate(context: vscode.ExtensionContext): void {
  console.log("nautilus IEC 61131-3 (iec-st) activated");

  // Roadmap wiring (see README):
  //   1. spawn the nautilus `st-lsp` binary
  //   2. new LanguageClient({ documentSelector: [{ language: "iec-st" }] })
  //   3. push a TextEditorDecorationType fed by the runtime's SSE/tag API for
  //      inline live values.
  // context.subscriptions.push(client.start());
}

export function deactivate(): void {
  // No-op until the language client is wired up.
}
