// The controller's auth token on the requests the extension makes to it
// (#819), and the rule for when it may go at all. Pure, so a test can
// check it; apiFetch.ts binds it to the settings.
//
// A controller with a token answers a name it doesn't trust (anything but
// localhost, an IP or a name in its NAUTILUS_ALLOWED_HOSTS) only when the
// request carries the token, reads included. But the extension connects on
// its own (live values open with an .st file), and nautilus.runtimeUrl can
// come from a cloned repository's .vscode/settings.json. So a token from
// the user's own settings goes only to a URL the user set (or the default),
// or to one they have said yes to once; never, unasked, to a URL a
// workspace chose.

/** init with Authorization: Bearer <token> added (when token is set),
 * keeping the caller's other headers and options. */
export function withToken(init: RequestInit | undefined, token: string): RequestInit {
  const out: RequestInit = { ...(init ?? {}) };
  if (!token) return out;
  const headers = new Headers(init?.headers);
  headers.set("Authorization", "Bearer " + token);
  const plain: Record<string, string> = {};
  headers.forEach((value, key) => {
    plain[key] = value;
  });
  out.headers = plain;
  return out;
}

/** The token header alone, for the node http stream subscription. */
export function tokenHeaders(token: string): Record<string, string> {
  return token ? { Authorization: "Bearer " + token } : {};
}

/** What WorkspaceConfiguration.inspect returns, the parts used here. */
export interface SettingScopes {
  defaultValue?: string;
  globalValue?: string;
  workspaceValue?: string;
  workspaceFolderValue?: string;
}

/** Whether to send the token, and when not, the origin to ask about. */
export interface TokenPlan {
  token: string;
  ask?: string;
}

/** The origin (scheme://host:port) of a URL, or "" when it doesn't parse. */
export function originOf(url: string): string {
  try {
    return new URL(url).origin;
  } catch {
    return "";
  }
}

/**
 * The token to send to url, given where each setting came from.
 *
 * - No token, or a target off the controller's origin: nothing. The
 *   token never goes anywhere but nautilus.runtimeUrl.
 * - The token is the workspace's own (set in its settings and in effect):
 *   it goes with the workspace's URL, since it is theirs to give.
 * - The token is the user's: it goes only when the URL in effect is the
 *   user's own setting or the default, or its origin is one the user has
 *   approved; otherwise nothing is sent and `ask` names the origin.
 */
export function tokenPlan(
  token: string,
  tokenScopes: SettingScopes,
  url: string,
  urlScopes: SettingScopes,
  approved: readonly string[],
  target?: string,
): TokenPlan {
  if (!token) return { token: "" };
  if (target !== undefined && originOf(target) !== originOf(url)) return { token: "" };
  const workspaceToken = tokenScopes.workspaceFolderValue ?? tokenScopes.workspaceValue;
  if (workspaceToken !== undefined && token === workspaceToken) return { token };
  const usersUrl = urlScopes.globalValue ?? urlScopes.defaultValue;
  if (url === usersUrl) return { token };
  const origin = originOf(url);
  if (origin && approved.includes(origin)) return { token };
  return { token: "", ask: origin || url };
}

/** The parts of a WorkspaceConfiguration ("nautilus") the rule reads. */
export interface ConfigView {
  get(key: string, defaultValue: string): string;
  inspect(key: string): SettingScopes | undefined;
}

/** tokenPlan for the nautilus settings in effect: the one place the
 * nautilus.token setting is read. */
export function planFromConfig(cfg: ConfigView, approved: readonly string[], target?: string): TokenPlan {
  const token = cfg.get("token", "");
  const url = cfg.get("runtimeUrl", "http://localhost:8080");
  return tokenPlan(token, cfg.inspect("token") ?? {}, url, cfg.inspect("runtimeUrl") ?? {}, approved, target);
}
