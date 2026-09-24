#!/usr/bin/env node
// Highest published version of the extension on each registry, per channel.
//
//   node .github/scripts/vscode-registry-versions.mjs [publisher.name]
//
// Prints (and appends to $GITHUB_OUTPUT when set) key=value lines:
//
//   mkt_ok=true|false      the Marketplace answered
//   mkt_pre=X.Y.Z          highest pre-release on the Marketplace
//   mkt_stable=X.Y.Z       highest stable release on the Marketplace
//   ovsx_ok, ovsx_pre, ovsx_stable   the same for Open VSX
//
// A channel with nothing published reads 0.0.0. A registry that could not be
// reached reads 0.0.0 with *_ok=false and a ::warning:: — callers decide
// whether that blocks them (the Marketplace gallery API times out for
// stretches, and Open VSX must not be held hostage to that).
//
// Channel detection:
//   Marketplace: `vsce show --json` lists versions[]; a pre-release carries
//     the property Microsoft.VisualStudio.Code.PreRelease = "true".
//   Open VSX: /api/v2/-/query?includeAllVersions=true returns one entry per
//     version with a boolean `preRelease`. (The top-level `version` and the
//     `latest` alias are NOT stable-only: with no stable published, `latest`
//     points at the newest pre-release.)
import { execFileSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';

const id = process.argv[2] || 'joyauto.vscode-iec';
const [namespace, name] = id.split('.');

const cmp = (a, b) => {
  const pa = a.split('.').map(Number), pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] - pb[i];
  return 0;
};
const max = (vs) => vs.reduce((m, v) => (cmp(v, m) > 0 ? v : m), '0.0.0');
const semver = /^\d+\.\d+\.\d+$/;

function split(entries) {
  const pre = [], stable = [];
  for (const { version, preRelease } of entries) {
    if (!semver.test(version)) continue;
    (preRelease ? pre : stable).push(version);
  }
  return { pre: max(pre), stable: max(stable) };
}

async function retry(what, fn) {
  let last;
  for (let attempt = 1; attempt <= 3; attempt++) {
    try { return await fn(); } catch (e) {
      last = e;
      console.error(`${what}: attempt ${attempt} failed: ${String(e.message || e).split('\n')[0]}`);
      if (attempt < 3) await new Promise((r) => setTimeout(r, 5000 * attempt));
    }
  }
  throw last;
}

async function marketplace() {
  const out = await retry('Marketplace', () =>
    execFileSync('npx', ['--yes', '@vscode/vsce', 'show', id, '--json'], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 256 << 20,
    }));
  const versions = JSON.parse(out).versions ?? [];
  return split(versions.map((v) => ({
    version: v.version,
    preRelease: (v.properties ?? []).some(
      (p) => p.key === 'Microsoft.VisualStudio.Code.PreRelease' && p.value === 'true'),
  })));
}

async function openVSX() {
  const entries = [];
  for (let offset = 0; ; ) {
    const url = `https://open-vsx.org/api/v2/-/query?extensionId=${id}` +
      `&includeAllVersions=true&size=100&offset=${offset}`;
    const page = await retry('Open VSX', async () => {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
      return res.json();
    });
    const exts = (page.extensions ?? []).filter(
      (e) => e.namespace === namespace && e.name === name);
    entries.push(...exts.map((e) => ({ version: e.version, preRelease: e.preRelease === true })));
    offset += (page.extensions ?? []).length;
    if (!page.extensions?.length || offset >= (page.totalSize ?? 0)) break;
  }
  return split(entries);
}

const lines = [];
for (const [key, label, fn] of [['mkt', 'Marketplace', marketplace], ['ovsx', 'Open VSX', openVSX]]) {
  try {
    const { pre, stable } = await fn();
    lines.push(`${key}_ok=true`, `${key}_pre=${pre}`, `${key}_stable=${stable}`);
  } catch (e) {
    console.log(`::warning::could not read ${id} from the ${label}: ${String(e.message || e).split('\n')[0]}`);
    lines.push(`${key}_ok=false`, `${key}_pre=0.0.0`, `${key}_stable=0.0.0`);
  }
}
console.log(lines.join('\n'));
if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, lines.join('\n') + '\n');
