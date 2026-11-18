// The controller's auth token on every request the extension makes to it
// (#819). A controller with a token answers a name it does not trust
// (anything but localhost, an IP or a name in its NAUTILUS_ALLOWED_HOSTS)
// only when the request carries the token, reads included, so a read
// without it would get 403 by name. Pure, so a test can check it.

/** init with Authorization: Bearer <token> added (when token is set),
 * keeping the caller's other headers. */
export function withToken(init: RequestInit | undefined, token: string): RequestInit {
  const out: RequestInit = { ...(init ?? {}) };
  if (!token) return out;
  const headers = new Headers(init?.headers);
  headers.set("Authorization", "Bearer " + token);
  out.headers = Object.fromEntries(headers.entries());
  return out;
}

/** The token header alone, for the node http stream subscription. */
export function tokenHeaders(token: string): Record<string, string> {
  return token ? { Authorization: "Bearer " + token } : {};
}
