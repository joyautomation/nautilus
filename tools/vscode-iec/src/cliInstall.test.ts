// Plain-Node tests for the CLI installer (no vscode dependency, no network).
// Run via `npm test`; CI also runs this file on macOS and Windows.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as zlib from "node:zlib";
import {
  assetName,
  checkMinVersion,
  compareSemver,
  describeApiError,
  extractBinary,
  GetOptions,
  HttpError,
  installLatestCli,
  parseChecksums,
  parseCliVersionOutput,
  parseSemver,
  pickCliRelease,
  Release,
  RELEASES_API,
  sha256,
  targetFor,
  tarFind,
  zipFind,
} from "./cliInstall";

// ── fixtures ───────────────────────────────────────────────────────────────

function tarHeader(name: string, size: number, type = "0", prefix = ""): Buffer {
  const h = Buffer.alloc(512);
  h.write(name, 0, 100, "utf8");
  h.write("0000755\0", 100);
  h.write("0000000\0", 108);
  h.write("0000000\0", 116);
  h.write(size.toString(8).padStart(11, "0") + "\0", 124);
  h.write("00000000000\0", 136);
  h.write("        ", 148);
  h.write(type, 156);
  h.write("ustar\0" + "00", 257, "latin1");
  if (prefix) h.write(prefix, 345, 155, "utf8");
  let sum = 0;
  for (const b of h) sum += b;
  h.write(sum.toString(8).padStart(6, "0") + "\0 ", 148, "latin1");
  return h;
}

function tarEntry(name: string, data: Buffer, type = "0", prefix = ""): Buffer {
  const pad = Buffer.alloc((512 - (data.length % 512)) % 512);
  return Buffer.concat([tarHeader(name, data.length, type, prefix), data, pad]);
}

function tar(...entries: Buffer[]): Buffer {
  return Buffer.concat([...entries, Buffer.alloc(1024)]);
}

/** A zip the way GoReleaser writes one: deflated, sizes only in the central
 * directory (bit 3 set, local sizes zero). CRCs are left zero; the reader
 * relies on the archive's SHA-256 instead. */
function zip(files: { name: string; data: Buffer; store?: boolean }[]): Buffer {
  const locals: Buffer[] = [];
  const centrals: Buffer[] = [];
  let offset = 0;
  for (const f of files) {
    const body = f.store ? f.data : zlib.deflateRawSync(f.data);
    const name = Buffer.from(f.name, "utf8");
    const lh = Buffer.alloc(30);
    lh.writeUInt32LE(0x04034b50, 0);
    lh.writeUInt16LE(20, 4);
    lh.writeUInt16LE(0x0008, 6);
    lh.writeUInt16LE(f.store ? 0 : 8, 8);
    lh.writeUInt16LE(name.length, 26);
    const dd = Buffer.alloc(16);
    dd.writeUInt32LE(0x08074b50, 0);
    dd.writeUInt32LE(body.length, 8);
    dd.writeUInt32LE(f.data.length, 12);
    locals.push(lh, name, body, dd);
    const ch = Buffer.alloc(46);
    ch.writeUInt32LE(0x02014b50, 0);
    ch.writeUInt16LE(20, 4);
    ch.writeUInt16LE(20, 6);
    ch.writeUInt16LE(0x0008, 8);
    ch.writeUInt16LE(f.store ? 0 : 8, 10);
    ch.writeUInt32LE(body.length, 20);
    ch.writeUInt32LE(f.data.length, 24);
    ch.writeUInt16LE(name.length, 28);
    ch.writeUInt32LE(offset, 42);
    centrals.push(ch, name);
    offset += lh.length + name.length + body.length + dd.length;
  }
  const cd = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(cd.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, cd, end]);
}

const BIN = Buffer.from("#!/bin/sh\necho nautilus 0.12.0\n".repeat(50));

// ── versions ───────────────────────────────────────────────────────────────

test("semver: ordering, pre-releases before releases, v prefix tolerated", () => {
  const cmp = (a: string, b: string) => Math.sign(compareSemver(parseSemver(a)!, parseSemver(b)!));
  assert.equal(cmp("0.11.0", "0.10.9"), 1);
  assert.equal(cmp("v0.11.0", "0.11.0"), 0);
  assert.equal(cmp("0.11.0-rc1", "0.11.0"), -1);
  assert.equal(cmp("0.11.0-rc.2", "0.11.0-rc.10"), -1);
  assert.equal(cmp("1.0.0", "0.99.99"), 1);
  // A go install of an untagged commit: a pseudo-version after v0.11.0.
  assert.equal(cmp("0.11.1-0.20260924120000-abcdef123456", "0.11.0"), 1);
  assert.equal(parseSemver("dev"), undefined);
  assert.equal(parseSemver("(devel)"), undefined);
});

test("checkMinVersion: old, ok, and dev builds (assumed current)", () => {
  assert.deepEqual(checkMinVersion("0.10.0", "0.11.0"), { kind: "old", version: "0.10.0", min: "0.11.0" });
  assert.equal(checkMinVersion("0.11.0", "0.11.0").kind, "ok");
  assert.equal(checkMinVersion("0.12.3", "0.11.0").kind, "ok");
  assert.equal(checkMinVersion("dev", "0.11.0").kind, "dev");
  assert.equal(checkMinVersion("(devel)", "0.11.0").kind, "dev");
});

test("parseCliVersionOutput: `naut version` output", () => {
  assert.equal(parseCliVersionOutput("nautilus 0.11.0\n"), "0.11.0");
  assert.equal(parseCliVersionOutput("nautilus dev\n"), "dev");
  assert.equal(parseCliVersionOutput("usage: ...\n"), undefined);
});

// ── releases ───────────────────────────────────────────────────────────────

const rel = (tag: string, extra: Partial<Release> = {}): Release => ({ tag_name: tag, assets: [], ...extra });

test("pickCliRelease: skips vscode-v tags, drafts and pre-releases; highest version wins", () => {
  const picked = pickCliRelease([
    rel("vscode-v0.12.0"),
    rel("v0.12.0-rc1", { prerelease: true }),
    rel("v0.13.0", { draft: true }),
    rel("v0.10.0"),
    rel("v0.11.0"),
    rel("nightly"),
  ]);
  assert.equal(picked?.tag_name, "v0.11.0");
  assert.equal(picked?.version, "0.11.0");
  assert.equal(pickCliRelease([rel("vscode-v1.0.0")]), undefined);
});

test("targetFor + assetName: GoReleaser's names per platform/arch", () => {
  const cases: [NodeJS.Platform, string, string | undefined][] = [
    ["linux", "x64", "nautilus_0.11.0_linux_amd64.tar.gz"],
    ["linux", "arm64", "nautilus_0.11.0_linux_arm64.tar.gz"],
    ["darwin", "arm64", "nautilus_0.11.0_darwin_arm64.tar.gz"],
    ["darwin", "x64", "nautilus_0.11.0_darwin_amd64.tar.gz"],
    ["win32", "x64", "nautilus_0.11.0_windows_amd64.zip"],
    ["win32", "arm64", "nautilus_0.11.0_windows_arm64.zip"],
    ["linux", "ia32", undefined],
    ["freebsd", "x64", undefined],
  ];
  for (const [platform, arch, want] of cases) {
    const t = targetFor(platform, arch);
    assert.equal(t && assetName("0.11.0", t), want, `${platform}/${arch}`);
  }
  assert.equal(targetFor("win32", "x64")?.exe, "naut.exe");
  assert.equal(targetFor("darwin", "arm64")?.exe, "naut");
});

test("parseChecksums: sha256sum format, binary-mode star, CRLF", () => {
  const a = "a".repeat(64);
  const b = "B".repeat(64);
  const sums = parseChecksums(`${a}  nautilus_0.11.0_linux_amd64.tar.gz\r\n${b} *nautilus_0.11.0_windows_amd64.zip\nnot a line\n`);
  assert.equal(sums.get("nautilus_0.11.0_linux_amd64.tar.gz"), a);
  assert.equal(sums.get("nautilus_0.11.0_windows_amd64.zip"), "b".repeat(64));
  assert.equal(sums.size, 2);
});

test("describeApiError: a rate limit says so", () => {
  const err = new HttpError("HTTP 403", 403, { "x-ratelimit-remaining": "0", "x-ratelimit-reset": "1790000000" });
  assert.match(describeApiError(err), /rate limit/);
  assert.equal(describeApiError(new HttpError("HTTP 500 from api.github.com", 500, {})), "HTTP 500 from api.github.com");
});

// ── archives ───────────────────────────────────────────────────────────────

test("tarFind: GoReleaser layout (naut beside LICENSE and README.md)", () => {
  const t = tar(tarEntry("LICENSE", Buffer.from("Apache")), tarEntry("README.md", Buffer.alloc(700, 1)), tarEntry("naut", BIN));
  assert.deepEqual(tarFind(t, "naut"), BIN);
  assert.equal(tarFind(t, "naut.exe"), undefined);
});

test("tarFind: ustar prefix, GNU long names, pax paths, directories skipped", () => {
  const long = "d/".repeat(60) + "naut";
  const pax = Buffer.from("15 path=p/naut\n");
  assert.deepEqual(tarFind(tar(tarEntry("naut", Buffer.alloc(0), "5"), tarEntry("naut", BIN, "0", "some/dir")), "naut"), BIN);
  assert.deepEqual(tarFind(tar(tarEntry("././@LongLink", Buffer.from(long + "\0"), "L"), tarEntry(long.slice(0, 99), BIN)), "naut"), BIN);
  assert.deepEqual(tarFind(tar(tarEntry("PaxHeader", pax, "x"), tarEntry("truncated-nam", BIN)), "naut"), BIN);
});

test("tarFind: a truncated archive is an error, not a short binary", () => {
  const t = tar(tarEntry("naut", BIN)).subarray(0, 700);
  assert.throws(() => tarFind(t, "naut"), /truncated/);
});

test("tarFind: reads what the system tar writes", { skip: process.platform === "win32" }, () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "naut-tar-"));
  try {
    fs.writeFileSync(path.join(dir, "naut"), BIN);
    fs.writeFileSync(path.join(dir, "LICENSE"), "x");
    execFileSync("tar", ["-czf", "a.tar.gz", "LICENSE", "naut"], { cwd: dir });
    const t = targetFor("linux", "x64")!;
    assert.deepEqual(extractBinary(fs.readFileSync(path.join(dir, "a.tar.gz")), t), BIN);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("zipFind: deflated with data descriptors, stored, and missing", () => {
  const z = zip([
    { name: "LICENSE", data: Buffer.from("Apache") },
    { name: "README.md", data: Buffer.alloc(3000, 7) },
    { name: "naut.exe", data: BIN },
  ]);
  assert.deepEqual(zipFind(z, "naut.exe"), BIN);
  assert.deepEqual(zipFind(zip([{ name: "bin/naut.exe", data: BIN, store: true }]), "naut.exe"), BIN);
  assert.equal(zipFind(z, "naut"), undefined);
  assert.throws(() => zipFind(Buffer.from("not a zip at all, not even close......"), "naut.exe"), /end-of-central/);
});

// ── install, end to end with a fake GitHub ─────────────────────────────────

function fakeGitHub(archive: Buffer, name: string, sum = sha256(archive)) {
  const base = "https://example.test/dl/";
  const releases: Release[] = [
    rel("vscode-v0.13.0", { assets: [{ name: "vscode-iec-0.13.0.vsix", browser_download_url: base + "vsix" }] }),
    rel("v0.12.0", {
      assets: [
        { name, browser_download_url: base + name, size: archive.length },
        { name: "checksums.txt", browser_download_url: base + "checksums.txt" },
      ],
    }),
    rel("v0.11.0"),
  ];
  const seen: string[] = [];
  const get = async (url: string, o: GetOptions): Promise<Buffer> => {
    seen.push(url);
    if (url === RELEASES_API) return Buffer.from(JSON.stringify(releases));
    if (url === base + "checksums.txt") return Buffer.from(`${sum}  ${name}\n${"0".repeat(64)}  other.zip\n`);
    if (url === base + name) {
      o.onProgress?.(archive.length / 2, archive.length);
      o.onProgress?.(archive.length, archive.length);
      return archive;
    }
    throw new Error(`unexpected ${url}`);
  };
  return { get, seen };
}

test("installLatestCli: downloads the newest v* release, verifies, installs executable", async () => {
  const archive = zlib.gzipSync(tar(tarEntry("LICENSE", Buffer.from("x")), tarEntry("naut", BIN)));
  const gh = fakeGitHub(archive, "nautilus_0.12.0_linux_amd64.tar.gz");
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "naut-install-"));
  try {
    const binDir = path.join(dir, "bin");
    const stages: number[] = [];
    const r = await installLatestCli({ binDir, platform: "linux", arch: "x64", get: gh.get, report: (_m, p) => p !== undefined && stages.push(p) });
    assert.deepEqual(r, { version: "0.12.0", path: path.join(binDir, "naut") });
    assert.deepEqual(fs.readFileSync(r.path), BIN);
    if (process.platform !== "win32") assert.ok(fs.statSync(r.path).mode & 0o100, "executable");
    assert.ok(!gh.seen.some((u) => u.includes("vsix")));
    assert.equal(stages[stages.length - 1], 100);
    assert.deepEqual([...stages].sort((a, b) => a - b), stages, "progress only goes forward");

    // Again, over the top of the first install (an update).
    await installLatestCli({ binDir, platform: "linux", arch: "x64", get: gh.get });
    assert.deepEqual(fs.readdirSync(binDir), ["naut"], "no temp or .old files left behind");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("installLatestCli: Windows zip", async () => {
  const archive = zip([{ name: "naut.exe", data: BIN }]);
  const gh = fakeGitHub(archive, "nautilus_0.12.0_windows_arm64.zip");
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "naut-install-"));
  try {
    const r = await installLatestCli({ binDir: dir, platform: "win32", arch: "arm64", get: gh.get });
    assert.equal(path.basename(r.path), "naut.exe");
    assert.deepEqual(fs.readFileSync(r.path), BIN);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("installLatestCli: refuses a checksum mismatch and leaves nothing behind", async () => {
  const archive = zlib.gzipSync(tar(tarEntry("naut", BIN)));
  const gh = fakeGitHub(archive, "nautilus_0.12.0_linux_amd64.tar.gz", "f".repeat(64));
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "naut-install-"));
  try {
    await assert.rejects(
      installLatestCli({ binDir: path.join(dir, "bin"), platform: "linux", arch: "x64", get: gh.get }),
      /checksum mismatch/
    );
    assert.ok(!fs.existsSync(path.join(dir, "bin")));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("installLatestCli: no build for this platform, and API failures, say why", async () => {
  await assert.rejects(installLatestCli({ binDir: "/nope", platform: "freebsd", arch: "x64" }), /no prebuilt naut for freebsd/);
  const limited = async (): Promise<Buffer> => {
    throw new HttpError("HTTP 403", 403, { "x-ratelimit-remaining": "0" });
  };
  await assert.rejects(installLatestCli({ binDir: "/nope", platform: "linux", arch: "x64", get: limited }), /couldn't list nautilus releases: .*rate limit/);
});
