// fetch() to the controller with nautilus.token attached (apiAuth.ts). It
// calls the global fetch at call time, so the playground build's rerouted
// fetch (web/engine.ts patchFetch) still sees every request.
import * as vscode from "vscode";
import { tokenHeaders, withToken } from "./apiAuth";

export function apiToken(): string {
  return vscode.workspace.getConfiguration("nautilus").get<string>("token", "");
}

export function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  return fetch(url, withToken(init, apiToken()));
}

export function apiStreamHeaders(): Record<string, string> {
  return tokenHeaders(apiToken());
}
