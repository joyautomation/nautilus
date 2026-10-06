// The force table, client side: pure helpers (no vscode import, so they
// unit-test under plain node) for the frame's `forces` block and the
// controller's force/SFC endpoints. See server/force.go for the API and
// runtime/force.go for what a force does.

/** Forced address (declared casing) → forced value, from a stream frame. */
export type ForceMap = ReadonlyMap<string, unknown>;

/** Read a frame's `forces` block. Absent means nothing is forced — the
 * server never delta-gates it, so an absent block is never "unchanged". */
export function parseForces(frame: { forces?: Record<string, unknown> } | undefined): Map<string, unknown> {
  const out = new Map<string, unknown>();
  for (const [name, value] of Object.entries(frame?.forces ?? {})) out.set(name, value);
  return out;
}

/** Lowercased address → value, the form the decoration path and the diagram
 * webviews look values up in (IEC identifiers are case-insensitive). */
export function lowerForces(forces: ForceMap): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [name, value] of forces) out[name.toLowerCase()] = value;
  return out;
}

/**
 * Is this address forced — itself, or as part of a forced whole? `P101.Speed`
 * is forced when `P101.Speed` or `P101` is; `P101` counts as forced when any
 * member of it is (its value on screen is not entirely the field's). Labels
 * may carry indexes (`Tbl[2]`); those resolve against their head.
 */
export function forcedAddress(forces: ForceMap | Record<string, unknown>, label: string): string | undefined {
  const keys = forces instanceof Map ? [...forces.keys()] : Object.keys(forces);
  if (keys.length === 0 || !label) return undefined;
  const want = label.toLowerCase();
  for (const k of keys) {
    const lk = k.toLowerCase();
    if (lk === want || want.startsWith(lk + ".") || want.startsWith(lk + "[") || lk.startsWith(want + ".")) return k;
  }
  return undefined;
}

/** The pill text for a forced value: an F badge ahead of the value. */
export function forcedPillText(formatted: string): string {
  return `F ${formatted}`;
}

/** The status-bar text for the force count, "" when nothing is forced. */
export function forceStatusText(n: number): string {
  if (n <= 0) return "";
  return `$(lock) ${n} force${n === 1 ? "" : "s"} active`;
}

/** The Live Values tree: a forced row's description. */
export function forcedDescription(formatted: string, actual?: string): string {
  return actual === undefined ? `F ${formatted}` : `F ${formatted} (actual ${actual})`;
}

/** A transition's command id: its declared name, or `t<line>` — the
 * transpiler's own identifier for an unnamed one (runtime/sfccmd.go). */
export function transitionId(t: { name?: string; line: number }): string {
  return t.name && t.name.length > 0 ? t.name : `t${t.line}`;
}

export type ApiResult = { ok: true; body: string } | { ok: false; status: number; message: string };

/** One write call against the controller, token included when configured.
 * fetchFn is injectable for tests. */
export async function controllerWrite(
  base: string,
  token: string,
  method: "POST" | "DELETE",
  path: string,
  body?: unknown,
  fetchFn: typeof fetch = fetch
): Promise<ApiResult> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = "Bearer " + token;
  const res = await fetchFn(base + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (res.ok) return { ok: true, body: text };
  return { ok: false, status: res.status, message: text.trim() || res.statusText };
}

export const forceApi = {
  force: (name: string, value: unknown) => ({ method: "POST" as const, path: "/api/forces", body: { name, value } }),
  unforce: (name: string) => ({ method: "DELETE" as const, path: "/api/forces/" + encodeURIComponent(name) }),
  clear: () => ({ method: "POST" as const, path: "/api/forces/clear" }),
  setStep: (step: string, pou?: string) => ({
    method: "POST" as const,
    path: "/api/sfc/step",
    body: pou ? { step, pou } : { step },
  }),
  fireTransition: (transition: string, pou?: string) => ({
    method: "POST" as const,
    path: "/api/sfc/transition",
    body: pou ? { transition, pou } : { transition },
  }),
};

/** The modal's text before a force: what, at which controller, from what. */
export function forceConfirmMessage(url: string, name: string, value: string, current?: string): string {
  const from = current === undefined ? "" : ` (now ${current})`;
  return `Force ${name}${from} to ${value} on ${url}? A force holds the value against the field and the logic until it is removed.`;
}

/** The modal's text before clearing the whole force table. */
export function clearForcesConfirmMessage(url: string, n: number): string {
  return `Remove all ${n} force${n === 1 ? "" : "s"} on ${url}? Every forced tag returns at once to what its driver or its logic says.`;
}
