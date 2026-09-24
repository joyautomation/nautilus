// Installing the nautilus CLI from a GitHub Release, without Go.
//
// Most people who open a .st file have never installed a Go toolchain, so
// `go install` can't be the only way in. This downloads the GoReleaser
// archive for this OS/arch, checks it against the release's checksums.txt,
// pulls `naut` out of it, and puts it where the resolver looks (the
// extension's global storage, bin/).
//
// No vscode import, and no runtime dependencies: tar.gz is gunzip (zlib) plus
// a tar reader that only has to find one regular file; zip is the central
// directory plus inflateRaw. Both are covered by node:test on every CI OS.
//
// Network access goes through node:https, not fetch: VS Code's extension host
// patches the http/https modules with its proxy support (the http.proxy
// setting, HTTPS_PROXY/HTTP_PROXY, and the OS proxy config), and does not do
// so reliably for the global fetch.

import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as https from "node:https";
import * as path from "node:path";
import * as zlib from "node:zlib";

export const REPO = "joyautomation/nautilus";
export const RELEASES_API = `https://api.github.com/repos/${REPO}/releases?per_page=50`;
export const RELEASES_PAGE = `https://github.com/${REPO}/releases`;

// ── versions ───────────────────────────────────────────────────────────────

export interface SemVer {
  major: number;
  minor: number;
  patch: number;
  /** Dot-separated pre-release identifiers ("rc1", or a Go pseudo-version's "0.2026…-abc"). */
  pre: string[];
}

export function parseSemver(v: string): SemVer | undefined {
  const m = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(v.trim());
  if (!m) return undefined;
  return { major: +m[1], minor: +m[2], patch: +m[3], pre: m[4] ? m[4].split(".") : [] };
}

/** Semver precedence: negative when a < b. A pre-release sorts before its release. */
export function compareSemver(a: SemVer, b: SemVer): number {
  for (const k of ["major", "minor", "patch"] as const) {
    if (a[k] !== b[k]) return a[k] - b[k];
  }
  if (!a.pre.length || !b.pre.length) return (a.pre.length ? -1 : 0) + (b.pre.length ? 1 : 0);
  for (let i = 0; i < Math.max(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i];
    const y = b.pre[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (x === y) continue;
    const nx = /^\d+$/.test(x);
    const ny = /^\d+$/.test(y);
    if (nx && ny) return +x - +y;
    if (nx !== ny) return nx ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

/** The version out of `naut version` ("nautilus 0.11.0", "nautilus dev"). */
export function parseCliVersionOutput(out: string): string | undefined {
  const m = /^\s*(?:nautilus|naut)\s+(\S+)/m.exec(out);
  return m?.[1];
}

export type VersionVerdict =
  /** At or above the minimum. */
  | { kind: "ok"; version: string }
  /** Older than the minimum. */
  | { kind: "old"; version: string; min: string }
  /** Not a release version (a local `go build` says "dev"): assumed current. */
  | { kind: "dev"; version: string };

export function checkMinVersion(version: string, min: string): VersionVerdict {
  const v = parseSemver(version);
  const m = parseSemver(min);
  if (!v || !m) return { kind: "dev", version };
  return compareSemver(v, m) < 0 ? { kind: "old", version, min } : { kind: "ok", version };
}

// ── releases ───────────────────────────────────────────────────────────────

export interface ReleaseAsset {
  name: string;
  browser_download_url: string;
  size?: number;
}

export interface Release {
  tag_name: string;
  draft?: boolean;
  prerelease?: boolean;
  assets: ReleaseAsset[];
}

/** The newest CLI release. The same repo also tags extension releases
 * (vscode-v*), and GitHub's "latest" flag follows whichever was published
 * last, so it can't be trusted: only tags that are a v and a semver count,
 * drafts and pre-releases never do, and the highest version wins regardless
 * of the order the API lists them in. */
export function pickCliRelease(releases: Release[]): (Release & { version: string }) | undefined {
  let best: (Release & { version: string; sv: SemVer }) | undefined;
  for (const r of releases) {
    if (r.draft || r.prerelease || !/^v\d/.test(r.tag_name)) continue;
    const sv = parseSemver(r.tag_name);
    if (!sv || sv.pre.length) continue;
    if (!best || compareSemver(sv, best.sv) > 0) best = { ...r, version: r.tag_name.slice(1), sv };
  }
  if (!best) return undefined;
  const { sv: _sv, ...rest } = best;
  return rest;
}

export interface Target {
  goos: "linux" | "darwin" | "windows";
  goarch: "amd64" | "arm64";
  archive: "tar.gz" | "zip";
  exe: string;
}

/** GoReleaser's names for this machine, or undefined when no build exists
 * for it (see .goreleaser.yaml: linux/darwin/windows × amd64/arm64). */
export function targetFor(platform: NodeJS.Platform, arch: string): Target | undefined {
  const goos = platform === "linux" ? "linux" : platform === "darwin" ? "darwin" : platform === "win32" ? "windows" : undefined;
  const goarch = arch === "x64" ? "amd64" : arch === "arm64" ? "arm64" : undefined;
  if (!goos || !goarch) return undefined;
  const win = goos === "windows";
  return { goos, goarch, archive: win ? "zip" : "tar.gz", exe: win ? "naut.exe" : "naut" };
}

/** The archive name .goreleaser.yaml's name_template produces. */
export function assetName(version: string, t: Target): string {
  return `nautilus_${version}_${t.goos}_${t.goarch}.${t.archive}`;
}

/** checksums.txt: "<sha256 hex>  <file name>" per line (sha256sum format). */
export function parseChecksums(text: string): Map<string, string> {
  const sums = new Map<string, string>();
  for (const line of text.split(/\r?\n/)) {
    const m = /^([0-9a-fA-F]{64})\s+\*?(\S.*?)\s*$/.exec(line);
    if (m) sums.set(m[2], m[1].toLowerCase());
  }
  return sums;
}

export function sha256(buf: Buffer): string {
  return crypto.createHash("sha256").update(buf).digest("hex");
}

// ── archives ───────────────────────────────────────────────────────────────

function cstr(buf: Buffer, start: number, len: number): string {
  const end = buf.indexOf(0, start);
  return buf.toString("utf8", start, end >= 0 && end < start + len ? end : start + len);
}

function octal(buf: Buffer, start: number, len: number): number {
  // GNU tar writes sizes over 8 GiB in base-256; nothing here is that big.
  if (buf[start] & 0x80) throw new Error("tar: base-256 sizes are not supported");
  const s = cstr(buf, start, len).trim();
  return s ? parseInt(s, 8) : 0;
}

/** The regular file whose base name is `name` from an uncompressed tar
 * (ustar, with GNU long names and pax path records honoured). */
export function tarFind(tar: Buffer, name: string): Buffer | undefined {
  let off = 0;
  let longName: string | undefined;
  while (off + 512 <= tar.length) {
    const h = tar.subarray(off, off + 512);
    if (h.every((b) => b === 0)) break;
    const size = octal(h, 124, 12);
    const type = String.fromCharCode(h[156] || 0x30);
    const prefix = h.toString("latin1", 257, 263) === "ustar\0" || h.toString("latin1", 257, 262) === "ustar" ? cstr(h, 345, 155) : "";
    const entry = longName ?? (prefix ? `${prefix}/${cstr(h, 0, 100)}` : cstr(h, 0, 100));
    longName = undefined;
    const body = off + 512;
    const data = tar.subarray(body, body + size);
    if (data.length < size) throw new Error("tar: archive is truncated");
    if (type === "L") {
      longName = cstr(data, 0, data.length);
    } else if (type === "x") {
      const m = /(?:^|\n)\d+ path=([^\n]*)\n/.exec(data.toString("utf8"));
      if (m) longName = m[1];
    } else if (type === "0" && path.posix.basename(entry) === name) {
      return Buffer.from(data);
    }
    off = body + Math.ceil(size / 512) * 512;
  }
  return undefined;
}

/** The file whose base name is `name` from a zip (stored or deflated). Sizes
 * come from the central directory, so streamed entries with data
 * descriptors (GoReleaser's) work. */
export function zipFind(zip: Buffer, name: string): Buffer | undefined {
  // End of central directory: the last "PK\5\6", within 64 KiB + 22 of the end.
  let eocd = -1;
  for (let i = zip.length - 22; i >= Math.max(0, zip.length - 22 - 0xffff); i--) {
    if (zip.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("zip: no end-of-central-directory record");
  const count = zip.readUInt16LE(eocd + 10);
  let cd = zip.readUInt32LE(eocd + 16);
  if (count === 0xffff || cd === 0xffffffff) throw new Error("zip: zip64 archives are not supported");
  for (let i = 0; i < count; i++) {
    if (zip.readUInt32LE(cd) !== 0x02014b50) throw new Error("zip: bad central directory");
    const method = zip.readUInt16LE(cd + 10);
    const compSize = zip.readUInt32LE(cd + 20);
    const size = zip.readUInt32LE(cd + 24);
    const nameLen = zip.readUInt16LE(cd + 28);
    const extraLen = zip.readUInt16LE(cd + 30);
    const commentLen = zip.readUInt16LE(cd + 32);
    const local = zip.readUInt32LE(cd + 42);
    const entry = zip.toString("utf8", cd + 46, cd + 46 + nameLen);
    cd += 46 + nameLen + extraLen + commentLen;
    if (entry.endsWith("/") || path.posix.basename(entry.replace(/\\/g, "/")) !== name) continue;
    if (zip.readUInt32LE(local) !== 0x04034b50) throw new Error("zip: bad local header");
    const start = local + 30 + zip.readUInt16LE(local + 26) + zip.readUInt16LE(local + 28);
    const raw = zip.subarray(start, start + compSize);
    const out = method === 0 ? Buffer.from(raw) : method === 8 ? zlib.inflateRawSync(raw) : undefined;
    if (!out) throw new Error(`zip: compression method ${method} is not supported`);
    if (out.length !== size) throw new Error("zip: size mismatch");
    return out;
  }
  return undefined;
}

export function extractBinary(archive: Buffer, t: Target): Buffer {
  const bin = t.archive === "zip" ? zipFind(archive, t.exe) : tarFind(zlib.gunzipSync(archive), t.exe);
  if (!bin) throw new Error(`the archive has no ${t.exe}`);
  return bin;
}

// ── download ───────────────────────────────────────────────────────────────

export class HttpError extends Error {
  constructor(message: string, readonly status: number, readonly headers: Record<string, string | string[] | undefined>) {
    super(message);
  }
}

export interface GetOptions {
  signal?: AbortSignal;
  headers?: Record<string, string>;
  onProgress?(received: number, total: number | undefined): void;
  /** Idle socket timeout (ms). */
  timeoutMs?: number;
}

const UA = "nautilus-vscode-iec";

/** GET a URL into memory, following redirects (release assets redirect to a CDN). */
export function httpsGet(url: string, o: GetOptions = {}, redirects = 5): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    if (o.signal?.aborted) return reject(new Error("cancelled"));
    const req = https.get(url, { headers: { "User-Agent": UA, ...o.headers }, signal: o.signal }, (res) => {
      const status = res.statusCode ?? 0;
      if (status >= 300 && status < 400 && res.headers.location) {
        res.resume();
        if (redirects <= 0) return reject(new Error(`too many redirects fetching ${url}`));
        // The API's Accept header must not follow onto the CDN.
        const { Accept: _a, ...rest } = o.headers ?? {};
        return resolve(httpsGet(new URL(res.headers.location, url).toString(), { ...o, headers: rest }, redirects - 1));
      }
      const chunks: Buffer[] = [];
      let received = 0;
      const total = Number(res.headers["content-length"]) || undefined;
      res.on("data", (c: Buffer) => {
        chunks.push(c);
        received += c.length;
        o.onProgress?.(received, total);
      });
      res.on("error", reject);
      res.on("end", () => {
        const body = Buffer.concat(chunks);
        if (status < 200 || status >= 300) {
          let msg = `HTTP ${status} from ${new URL(url).host}`;
          try {
            const j = JSON.parse(body.toString("utf8")) as { message?: string };
            if (j.message) msg += `: ${j.message}`;
          } catch {
            /* not JSON */
          }
          return reject(new HttpError(msg, status, res.headers));
        }
        resolve(body);
      });
    });
    req.setTimeout(o.timeoutMs ?? 30_000, () => req.destroy(new Error(`no response from ${new URL(url).host} for ${Math.round((o.timeoutMs ?? 30_000) / 1000)} s`)));
    req.on("error", (err) => reject(o.signal?.aborted ? new Error("cancelled") : err));
  });
}

/** A readable reason for a failed releases lookup; GitHub's unauthenticated
 * API allows 60 requests an hour per IP, which a shared office NAT can use up. */
export function describeApiError(err: unknown): string {
  if (err instanceof HttpError && (err.status === 403 || err.status === 429)) {
    const remaining = err.headers["x-ratelimit-remaining"];
    const reset = Number(err.headers["x-ratelimit-reset"]);
    if (remaining === "0" || err.status === 429) {
      const when = reset ? ` (resets at ${new Date(reset * 1000).toLocaleTimeString()})` : "";
      return `GitHub's API rate limit for this network is used up${when}`;
    }
  }
  return err instanceof Error ? err.message : String(err);
}

// ── install ────────────────────────────────────────────────────────────────

export interface InstallOptions {
  /** Where `naut` goes (created if missing). */
  binDir: string;
  platform?: NodeJS.Platform;
  arch?: string;
  signal?: AbortSignal;
  /** Stage messages and download progress, 0–100 over the whole install. */
  report?(message: string, percent?: number): void;
  /** Injected for tests; httpsGet otherwise. */
  get?: (url: string, o: GetOptions) => Promise<Buffer>;
}

export interface Installed {
  version: string;
  path: string;
}

export async function latestCliRelease(o: Pick<InstallOptions, "signal" | "get"> = {}): Promise<Release & { version: string }> {
  const get = o.get ?? httpsGet;
  let body: Buffer;
  try {
    body = await get(RELEASES_API, { signal: o.signal, headers: { Accept: "application/vnd.github+json" }, timeoutMs: 15_000 });
  } catch (err) {
    throw new Error(`couldn't list nautilus releases: ${describeApiError(err)}`);
  }
  const release = pickCliRelease(JSON.parse(body.toString("utf8")) as Release[]);
  if (!release) throw new Error("no nautilus CLI release found on GitHub");
  return release;
}

/** Download, verify, and install the latest CLI release into binDir. */
export async function installLatestCli(o: InstallOptions): Promise<Installed> {
  const get = o.get ?? httpsGet;
  const t = targetFor(o.platform ?? process.platform, o.arch ?? process.arch);
  if (!t) throw new Error(`no prebuilt naut for ${o.platform ?? process.platform}/${o.arch ?? process.arch}; build it with go install`);

  o.report?.("Finding the latest release…", 0);
  const release = await latestCliRelease(o);
  const want = assetName(release.version, t);
  const asset = release.assets.find((a) => a.name === want);
  const sumsAsset = release.assets.find((a) => a.name === "checksums.txt");
  if (!asset) throw new Error(`release ${release.tag_name} has no ${want}`);
  if (!sumsAsset) throw new Error(`release ${release.tag_name} has no checksums.txt; refusing to install an unverified binary`);

  const expected = parseChecksums((await get(sumsAsset.browser_download_url, { signal: o.signal })).toString("utf8")).get(want);
  if (!expected) throw new Error(`checksums.txt has no entry for ${want}; refusing to install an unverified binary`);

  o.report?.(`Downloading naut ${release.version}…`, 5);
  let last = 5;
  const archive = await get(asset.browser_download_url, {
    signal: o.signal,
    onProgress: (got, total) => {
      const pct = 5 + Math.floor((85 * got) / (total ?? asset.size ?? got));
      if (pct > last) {
        o.report?.(`Downloading naut ${release.version}… ${(got / 1e6).toFixed(1)} MB`, pct);
        last = pct;
      }
    },
  });

  const actual = sha256(archive);
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${want} (expected ${expected}, got ${actual}); not installing it`);
  }
  if (o.signal?.aborted) throw new Error("cancelled");

  o.report?.("Installing…", 92);
  const bin = extractBinary(archive, t);
  const dest = path.join(o.binDir, t.exe);
  placeBinary(bin, dest);
  o.report?.(`Installed naut ${release.version}`, 100);
  return { version: release.version, path: dest };
}

/** Write the binary next to its destination, then swap it in. On Windows a
 * running naut.exe (the language server) can't be overwritten but can be
 * renamed, so the old one is moved aside first and removed when possible. */
export function placeBinary(bin: Buffer, dest: string): void {
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  const tmp = `${dest}.download-${process.pid}`;
  fs.writeFileSync(tmp, bin, { mode: 0o755 });
  fs.chmodSync(tmp, 0o755);
  const old = `${dest}.old`;
  try {
    fs.rmSync(old, { force: true });
  } catch {
    /* still running from a previous update; the rename below reuses a new name */
  }
  let moved: string | undefined;
  if (fs.existsSync(dest)) {
    moved = fs.existsSync(old) ? `${dest}.old-${Date.now()}` : old;
    fs.renameSync(dest, moved);
  }
  try {
    fs.renameSync(tmp, dest);
  } catch (err) {
    if (moved) fs.renameSync(moved, dest);
    fs.rmSync(tmp, { force: true });
    throw err;
  }
  if (moved) {
    try {
      fs.rmSync(moved, { force: true });
    } catch {
      /* in use (Windows); cleaned up by the next install */
    }
  }
}
