// A Node built-in the web build bundles but never reaches at run time
// (fs, os, net, zlib, crypto, module, esbuild): every feature that uses one
// is a real-machine feature, gated on IS_WEB. Calling into it is a bug, and
// says so.
const fail = (name: string) => () => {
  throw new Error(`${name} is not available in the browser playground`);
};
const handler: ProxyHandler<Record<string, unknown>> = {
  get(_t, prop) {
    if (prop === "__esModule") return true;
    if (prop === "default") return stub;
    if (prop === "promises") return stub;
    if (prop === "constants") return {};
    if (prop === "platform") return () => "browser";
    if (prop === "tmpdir" || prop === "homedir") return () => "/tmp";
    if (prop === "EOL") return "\n";
    if (prop === "sep") return "/";
    if (prop === "existsSync") return () => false;
    return fail(String(prop));
  },
};
const stub = new Proxy({}, handler);
export = stub;
