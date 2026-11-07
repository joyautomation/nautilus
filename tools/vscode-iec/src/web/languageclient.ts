// vscode-languageclient/node for the web build: the same LanguageClient
// constructor extension.ts calls, connected to `naut lsp` inside the
// playground engine over the bridge instead of a child process.
import { LanguageClient as BrowserLanguageClient, type LanguageClientOptions } from "vscode-languageclient/browser";
import { engine } from "./engine";

export * from "vscode-languageclient/browser";
export type ServerOptions = unknown;

/** A Worker-shaped port onto the engine's language server: what the
 * browser client's BrowserMessageReader/Writer use. */
function enginePort(): Worker {
  let onmessage: ((ev: MessageEvent) => void) | null = null;
  const listener = (msg: unknown) => onmessage?.({ data: msg } as MessageEvent);
  engine.lspListeners.add(listener);
  return {
    postMessage: (msg: unknown) => engine.lspSend(msg),
    set onmessage(fn: ((ev: MessageEvent) => void) | null) {
      onmessage = fn;
    },
    get onmessage() {
      return onmessage;
    },
    addEventListener() {},
    removeEventListener() {},
    terminate: () => engine.lspListeners.delete(listener),
  } as unknown as Worker;
}

export class LanguageClient extends BrowserLanguageClient {
  constructor(id: string, name: string, _server: ServerOptions, clientOptions: LanguageClientOptions) {
    super(id, name, clientOptions, enginePort());
  }
}
