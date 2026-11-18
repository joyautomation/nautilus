// fetch() to the controller with nautilus.token attached when the rule in
// apiAuth.ts allows it. It calls the global fetch at call time, so the
// playground build's rerouted fetch (web/engine.ts patchFetch) still sees
// every request. This is the only module that reads nautilus.token.
import * as vscode from "vscode";
import { tokenHeaders, tokenPlan, withToken } from "./apiAuth";

const APPROVED_KEY = "nautilus.tokenOrigins";
let state: vscode.Memento | undefined;
const approvedThisSession = new Set<string>();
const askedThisSession = new Set<string>();

/** Called from activate: where approved origins are remembered. */
export function initApiAuth(context: vscode.ExtensionContext): void {
  state = context.globalState;
}

function approved(): string[] {
  return [...(state?.get<string[]>(APPROVED_KEY, []) ?? []), ...approvedThisSession];
}

/** The token to send to the configured controller, or "" (see apiAuth.ts).
 * With target, "" unless target is on the configured controller's origin:
 * the token never goes anywhere else. */
export function apiToken(target?: string): string {
  const cfg = vscode.workspace.getConfiguration("nautilus");
  const token = cfg.get<string>("token", "");
  const url = cfg.get<string>("runtimeUrl", "http://localhost:8080");
  const plan = tokenPlan(token, cfg.inspect<string>("token") ?? {}, url, cfg.inspect<string>("runtimeUrl") ?? {}, approved(), target);
  if (plan.ask) void askOnce(plan.ask);
  return plan.token;
}

/** Ask, once a session per origin, whether the user's token may go to a
 * controller URL the workspace chose. Yes is remembered. */
async function askOnce(origin: string): Promise<void> {
  if (askedThisSession.has(origin)) return;
  askedThisSession.add(origin);
  const send = `Send it to ${origin}`;
  const pick = await vscode.window.showWarningMessage(
    `This workspace points nautilus.runtimeUrl at ${origin}. Your nautilus.token comes from your user settings, so it is not sent there until you say so. A controller with a token answers a host name it doesn't trust only with the token.`,
    send,
    "Don't send",
  );
  if (pick !== send) return;
  approvedThisSession.add(origin);
  await state?.update(APPROVED_KEY, [...new Set([...(state.get<string[]>(APPROVED_KEY, []) ?? []), origin])]);
}

export function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  return fetch(url, withToken(init, apiToken(url)));
}

export function apiStreamHeaders(url: string): Record<string, string> {
  return tokenHeaders(apiToken(url));
}
