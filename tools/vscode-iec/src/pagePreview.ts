// "Preview HMI Page": the active file's SvelteKit route, served by the
// project's OWN `npm run dev` (started in a VS Code terminal on a port we
// pick, reused across invocations), opened in VS Code's Simple Browser
// beside the text editor. Vite's HMR keeps the page current as you type;
// the page's values come from whatever controller the dev server proxies
// `/api` to — `nautilus.runtimeUrl`, passed as CONTROLLER_URL, the way the
// examples' vite.config.ts expect it.
//
// Why not compile the page like a component (componentPreview.ts): a page
// is its layout chain + `$lib`/`$app` + theme CSS + the realtime client,
// i.e. the whole app. The dev server already is that, so the page is
// previewed for use rather than in isolation. See pagePreviewLogic.ts for
// the pure parts.

import * as vscode from "vscode";
import * as fs from "fs";
import * as net from "net";
import * as path from "path";
import { cliCommand } from "./cli";
import { shellLine } from "./logixDeployLogic";
import {
  svelteKitRootFor,
  routeForFile,
  routeHasParams,
  devServerArgs,
  pageUrl,
  nautilusProjectAbove,
} from "./pagePreviewLogic";

type DevServer = { port: number; terminal: vscode.Terminal };

const exists = (p: string) => fs.existsSync(p);

/** One dev server per SvelteKit root, keyed by its directory. */
const servers = new Map<string, DevServer>();

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => (port ? resolve(port) : reject(new Error("no free port"))));
    });
  });
}

function portOpen(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ port, host: "localhost" });
    const done = (ok: boolean) => {
      sock.destroy();
      resolve(ok);
    };
    sock.once("connect", () => done(true));
    sock.once("error", () => done(false));
    sock.setTimeout(1000, () => done(false));
  });
}

async function waitForPort(port: number, ms: number, token: vscode.CancellationToken): Promise<boolean> {
  const until = Date.now() + ms;
  while (Date.now() < until && !token.isCancellationRequested) {
    if (await portOpen(port)) return true;
    await new Promise((r) => setTimeout(r, 500));
  }
  return false;
}

function runtimeUrl(): string {
  return vscode.workspace.getConfiguration("nautilus").get<string>("runtimeUrl", "http://localhost:8080");
}

async function controllerUp(url: string): Promise<boolean> {
  try {
    const ctl = new AbortController();
    const t = setTimeout(() => ctl.abort(), 2000);
    const res = await fetch(`${url.replace(/\/$/, "")}/api/state`, { signal: ctl.signal });
    clearTimeout(t);
    return res.ok;
  } catch {
    return false;
  }
}

/** The dev server for `root`: the one already running if its terminal is
 * still open and its port answers, else a fresh `npm run dev` in a new
 * terminal. Returns null when the server never came up (or the wait was
 * cancelled); the terminal stays open so its output explains why. */
async function ensureServer(root: string): Promise<DevServer | null> {
  const have = servers.get(root);
  if (have && have.terminal.exitStatus === undefined && (await portOpen(have.port))) return have;
  if (have) servers.delete(root);

  const port = await freePort();
  const terminal = vscode.window.createTerminal({
    name: `hmi dev: ${path.basename(root)}`,
    cwd: root,
    env: { CONTROLLER_URL: runtimeUrl() },
  });
  terminal.show(true);
  terminal.sendText(shellLine("npm", devServerArgs(port), process.platform === "win32"));
  const server = { port, terminal };
  servers.set(root, server);

  const up = await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: `nautilus: starting the HMI dev server in ${path.basename(root)}…`,
      cancellable: true,
    },
    (_progress, token) => waitForPort(port, 90_000, token)
  );
  if (!up) {
    servers.delete(root);
    void vscode.window.showErrorMessage(
      `nautilus: the HMI dev server in ${path.basename(root)} did not come up on port ${port} — see its terminal (is \`npm install\` done there?).`
    );
    return null;
  }
  return server;
}

/** Open `url` beside the text editor — Simple Browser when VS Code has
 * it (it ships built in), the system browser otherwise. Through
 * asExternalUri so a Remote-SSH / Codespaces window reaches the forwarded
 * port rather than its own localhost. */
async function openBeside(url: string): Promise<void> {
  const ext = await vscode.env.asExternalUri(vscode.Uri.parse(url));
  const cmds = await vscode.commands.getCommands(true);
  if (cmds.includes("simpleBrowser.api.open")) {
    await vscode.commands.executeCommand("simpleBrowser.api.open", ext, {
      viewColumn: vscode.ViewColumn.Beside,
      preserveFocus: true,
    });
  } else {
    await vscode.env.openExternal(ext);
  }
}

/** When no controller answers at nautilus.runtimeUrl, say so once and
 * offer to start `naut run .` in the nautilus project above the HMI —
 * the page renders either way, just disconnected. */
async function offerController(root: string): Promise<void> {
  const url = runtimeUrl();
  if (await controllerUp(url)) return;
  const project = nautilusProjectAbove(root, exists);
  const RUN = "Start naut run";
  const pick = await vscode.window.showInformationMessage(
    `nautilus: no controller answers at ${url} — the page will show disconnected until one does.`,
    ...(project ? [RUN] : [])
  );
  if (pick === RUN && project) {
    const term = vscode.window.createTerminal({ name: `naut run: ${path.basename(project)}`, cwd: project });
    term.show(true);
    term.sendText(shellLine(cliCommand(), ["run", "."], process.platform === "win32"));
  }
}

async function previewPage(): Promise<void> {
  const doc = vscode.window.activeTextEditor?.document;
  if (!doc || doc.uri.scheme !== "file") {
    void vscode.window.showWarningMessage("nautilus: open a file under a SvelteKit project's src/routes first.");
    return;
  }
  const root = svelteKitRootFor(doc.uri.fsPath, exists);
  if (!root) {
    void vscode.window.showWarningMessage(
      `nautilus: ${path.basename(doc.uri.fsPath)} is not inside a SvelteKit project (no svelte.config.* above it).`
    );
    return;
  }
  const route = routeForFile(doc.uri.fsPath, root);
  if (route === null) {
    void vscode.window.showWarningMessage(
      `nautilus: ${path.basename(doc.uri.fsPath)} is not under ${path.relative(path.dirname(root), path.join(root, "src", "routes"))} — a page preview needs a route.`
    );
    return;
  }
  if (routeHasParams(route)) {
    void vscode.window.showInformationMessage(
      `nautilus: ${route} has a [param] — opening it as written; edit the address in the preview to fill it in.`
    );
  }
  if (!vscode.workspace.isTrusted) {
    void vscode.window.showWarningMessage("nautilus: page preview runs the project's own dev server — trust the workspace first.");
    return;
  }

  const server = await ensureServer(root);
  if (!server) return;
  await openBeside(pageUrl(server.port, route));
  void offerController(root);
}

export function registerPagePreview(): vscode.Disposable {
  return vscode.Disposable.from(
    vscode.commands.registerCommand("nautilus.page.preview", () => previewPage()),
    vscode.window.onDidCloseTerminal((t) => {
      for (const [root, s] of servers) if (s.terminal === t) servers.delete(root);
    })
  );
}
