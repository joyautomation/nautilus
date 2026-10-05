// Bakes the latest GREEN verification evidence into the docs site.
//
// Runs as `prebuild` (and `predev`). With GITHUB_TOKEN it finds the latest
// successful run on main of each evidence workflow, downloads its artifact,
// and writes:
//   public/verified/<set>/…        clips (.mp4), evidence PNGs, manifest.json
//   src/data/verified.json         {generatedAt, sets, rows} for the pages
// where <set> is nightly (rig-nightly.yml, rig-out-*), demo (rig-demo.yml,
// rig-demo-*) or gestures (ci.yml, gesture-clips). Rows come from the gesture
// inventory (tools/vscode-iec/webview-ui/gesture-harness/INVENTORY.md) joined
// to the manifest items. Manifest schema 1 is the contract with the rig; when
// an artifact predates it, the manifest is synthesised from the rig's TSVs
// (smoke/results.tsv, selftest/verbs-selftest.tsv) or the harness's
// <suite>/results.json.
//
// The build must always pass, so nothing here exits non-zero:
//   - no token and data already present → keep it untouched (the deploy job
//     fetches on the runner, then `docker build` runs this again tokenless);
//   - no token and no data → write the empty shape (pages say "no verified
//     run yet");
//   - an API/download error for one set → keep that set's previous data if
//     there is any, else null.
//
// Zero dependencies: node 22+ fetch, and the `unzip` binary (ubuntu-latest,
// alpine's busybox, and most desktops have one).

import { execFileSync } from 'node:child_process';
import { createWriteStream, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, copyFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, relative, sep } from 'node:path';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';

const WEB = dirname(dirname(fileURLToPath(import.meta.url)));
const REPO_ROOT = dirname(WEB);
const DATA_FILE = join(WEB, 'src', 'data', 'verified.json');
const PUBLIC_DIR = join(WEB, 'public', 'verified');
const INVENTORY = join(REPO_ROOT, 'tools', 'vscode-iec', 'webview-ui', 'gesture-harness', 'INVENTORY.md');
const REPO = process.env.GITHUB_REPOSITORY || 'joyautomation/nautilus';
const TOKEN = process.env.GITHUB_TOKEN || process.env.GH_TOKEN || '';
const API = 'https://api.github.com';
const CAP_BYTES = 100 * 1024 * 1024; // per set
const DEADLINE = Date.now() + 110_000; // keep the whole fetch under ~2 minutes

const SETS = [
  { key: 'nightly', workflow: 'rig-nightly.yml', match: (n) => n.startsWith('rig-out-') },
  { key: 'demo', workflow: 'rig-demo.yml', match: (n) => n.startsWith('rig-demo-') },
  { key: 'gestures', workflow: 'ci.yml', match: (n) => n === 'gesture-clips' },
];

const log = (msg) => console.log(`[verified] ${msg}`);

// ---------------------------------------------------------------- inventory

const SECTION_BY_PREFIX = { C: 'commands', F: 'fbd', L: 'ladder', S: 'sfc', M: 'mimic', P: 'component', X: 'other' };
const NONE = /^(—|-|–)?$/;

/** Split a markdown table line into trimmed cells (no leading/trailing pipe). */
function cells(line) {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, '|'));
}

export function parseInventory(md) {
  const rows = [];
  let header = null;
  for (const line of md.split('\n')) {
    if (!line.trim().startsWith('|')) {
      header = null;
      continue;
    }
    const c = cells(line);
    if (c[0] === 'id') {
      header = c.map((h) => h.toLowerCase());
      continue;
    }
    if (!header || /^-+$/.test(c[0].replace(/:/g, '')) || !/^[A-Z]\d{2}$/.test(c[0])) continue;
    const get = (name) => {
      const i = header.indexOf(name);
      const v = i < 0 ? '' : c[i] ?? '';
      return NONE.test(v) ? '' : v;
    };
    rows.push({
      id: c[0],
      section: SECTION_BY_PREFIX[c[0][0]] ?? 'other',
      area: get('area'),
      gesture: get('gesture'),
      source: get('source of the claim'),
      webviewTest: get('webview test'),
      rigVerb: get('rig verb'),
      smoke: get('smoke'),
    });
  }
  return rows;
}

/** Remove (…) notes, nested one level deep. */
const stripParens = (s) => s.replace(/\([^()]*(\([^()]*\)[^()]*)*\)/g, ' ');

/** `fbd_pin_el / g_click`, `fbd_wire (asserts)`, `ld_add_contact, float_edit` → verb names. */
export function parseVerbCell(cell) {
  return stripParens(cell)
    .split(/[,/;]/)
    .map((s) => s.trim().split(/\s+/)[0])
    .filter((s) => /^[a-z][a-z0-9_]*$/.test(s));
}

/** `03, 07`, `04 (menu entry present only)`, `13 (.st, .fbd; not .sfc)` → ['03','07']. */
export function parseSmokeCell(cell) {
  return [...stripParens(cell).matchAll(/\b(\d{2})\b/g)].map((m) => m[1]);
}

/** `diagram.test.mjs: A; B; fbd-drag.test.mjs: C` → [{suite:'diagram', text:'A'}, …]. */
export function parseWebviewCell(cell) {
  const out = [];
  let suite = null;
  // Notes in (…) may hold their own ';', so drop them before splitting.
  for (let seg of stripParens(cell).split(';')) {
    seg = seg.trim();
    const m = seg.match(/^([\w-]+)\.test\.mjs:\s*(.*)$/);
    if (m) {
      suite = m[1];
      seg = m[2];
    }
    if (seg) out.push({ suite, text: seg });
  }
  return out;
}

const words = (s) =>
  stripParens(s)
    .toLowerCase()
    .replace(/[^a-z0-9+]+/g, ' ')
    .trim()
    .split(' ')
    .filter(Boolean);

const containsSeq = (hay, needle) => {
  if (!needle.length) return false;
  outer: for (let i = 0; i + needle.length <= hay.length; i++) {
    for (let j = 0; j < needle.length; j++) if (hay[i + j] !== needle[j]) continue outer;
    return true;
  }
  return false;
};

/** Does the inventory fragment name this test? Pieces split on "..." must all appear, in order. */
function fragmentMatches(fragment, testName) {
  const hay = words(testName);
  // "ports-edit tests" → every test whose name starts with "ports-edit".
  const family = fragment.trim().match(/^(.+?)\s+tests$/i);
  if (family) return containsSeq(hay.slice(0, words(family[1]).length), words(family[1]));
  const pieces = fragment.split(/\.\.\.|…/).map(words).filter((p) => p.length);
  if (!pieces.length) return false;
  // One-word fragments ("Restore:", "Theme:") only match as the name's prefix.
  if (pieces.length === 1 && pieces[0].length < 3) return hay.slice(0, pieces[0].length).join(' ') === pieces[0].join(' ');
  return pieces.every((p) => containsSeq(hay, p));
}

function matchGestures(row, gestureItems) {
  if (!gestureItems.length) return [];
  const hit = new Set();
  for (const { suite, text } of parseWebviewCell(row.webviewTest)) {
    const pool = gestureItems.filter((g) => !suite || g.suite === suite);
    let found = pool.filter((g) => fragmentMatches(text, g.test ?? ''));
    // "M17 Ctrl+Z …" names another row's test by its id; wording may drift.
    const idTag = text.match(/^([A-Z]\d{2})\b/);
    if (!found.length && idTag) found = pool.filter((g) => (g.test ?? '').startsWith(`${idTag[1]} `));
    if (!found.length) {
      // "a, b, c" lists: try each comma piece that is specific enough.
      for (const piece of text.split(',')) {
        if (words(piece).length >= 3) found.push(...pool.filter((g) => fragmentMatches(piece, g.test ?? '')));
      }
    }
    found.forEach((g) => hit.add(g));
  }
  // Tests that name the row id themselves ("P01 component: …", "(F02)").
  for (const g of gestureItems) {
    if (new RegExp(`(^|[^A-Za-z0-9])${row.id}([^0-9]|$)`).test(g.test ?? '')) hit.add(g);
  }
  return [...hit];
}

export function joinRows(rows, sets) {
  const verbItems = ['demo', 'nightly'].flatMap((k) => (sets[k]?.items ?? []).filter((i) => i.kind === 'verb'));
  const smokeItems = ['demo', 'nightly'].flatMap((k) => (sets[k]?.items ?? []).filter((i) => i.kind === 'smoke'));
  const gestureItems = (sets.gestures?.items ?? []).filter((i) => i.kind === 'gesture');
  return rows.map((row) => {
    const verbNames = parseVerbCell(row.rigVerb);
    const smokeNums = parseSmokeCell(row.smoke);
    return {
      ...row,
      refs: { verbs: verbNames, smokes: smokeNums },
      verbs: verbItems.filter((i) => verbNames.some((v) => i.id === v || i.id.startsWith(`${v}-`))),
      smokes: smokeItems.filter((i) => smokeNums.some((n) => i.id === n || i.id.startsWith(`${n}-`))),
      gestures: matchGestures(row, gestureItems),
    };
  });
}

// ------------------------------------------------------------ GitHub fetch

async function gh(path, { raw = false } = {}) {
  const ms = Math.max(1000, Math.min(60_000, DEADLINE - Date.now()));
  const res = await fetch(path.startsWith('http') ? path : `${API}${path}`, {
    headers: {
      authorization: `Bearer ${TOKEN}`,
      accept: 'application/vnd.github+json',
      'x-github-api-version': '2022-11-28',
      'user-agent': 'nautilus-docs-fetch-verified',
    },
    redirect: 'follow',
    signal: AbortSignal.timeout(ms),
  });
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`GitHub ${res.status} ${res.statusText} for ${path}`);
  return raw ? res : res.json();
}

async function latestGreenRun(workflow) {
  const r = await gh(`/repos/${REPO}/actions/workflows/${workflow}/runs?status=success&branch=main&per_page=1`);
  return r?.workflow_runs?.[0] ?? null; // null: workflow missing or no green run
}

function unzip(zip, dir) {
  try {
    execFileSync('unzip', ['-q', '-o', zip, '-d', dir], { stdio: ['ignore', 'ignore', 'pipe'] });
  } catch (e) {
    if (e.code === 'ENOENT') throw new Error('`unzip` is not on PATH — install it (apt-get install unzip) to bake verified clips');
    throw new Error(`unzip failed: ${String(e.stderr ?? e.message).trim()}`);
  }
}

function walk(dir) {
  const out = [];
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, ent.name);
    if (ent.isDirectory()) out.push(...walk(p));
    else out.push(p);
  }
  return out;
}

// ------------------------------------------------------ manifest (schema 1)

const rel = (base, p) => relative(base, p).split(sep).join('/');

function worstVerdict(vs) {
  const order = ['FAIL', 'XPASS', 'WARN', 'XFAIL', 'PASS'];
  for (const v of order) if (vs.includes(v)) return v;
  return vs.length ? vs[0] : 'SKIP';
}

/** Build a schema-1 manifest from the rig's TSVs when the run predates manifest.json. */
function synthRig(key, root, files) {
  const results = files.find((f) => f.endsWith(`${sep}smoke${sep}results.tsv`));
  const verbsTsv = files.find((f) => f.endsWith(`${sep}selftest${sep}verbs-selftest.tsv`));
  if (!results && !verbsTsv) return null;
  const base = dirname(dirname(results ?? verbsTsv));
  const has = (p) => existsSync(join(base, p));
  const run = { sha: null, naut: null, vsix: null, vscode: null, date: null, pace: null, frame: null, runId: null, host: null };
  const items = [];
  const parseHeader = (line) => {
    // "# nautilus <sha> · vscode-iec.vsix · VS Code 1.139.1 · 2026-10-04T00:43:08+00:00"
    const sha = line.match(/\b([0-9a-f]{40})\b/);
    const vsc = line.match(/VS Code ([\w.-]+)/);
    const date = line.match(/(\d{4}-\d{2}-\d{2}T[\d:]+(?:Z|[+-]\d{2}:\d{2}))/);
    if (sha) run.sha ??= sha[1];
    if (vsc) run.vscode ??= vsc[1];
    if (date) run.date ??= new Date(date[1]).toISOString();
  };
  if (results) {
    const byId = new Map();
    for (const line of readFileSync(results, 'utf8').split('\n')) {
      if (line.startsWith('#')) {
        parseHeader(line);
        continue;
      }
      const [id, verdict, text, png] = line.split('\t');
      if (!id || !verdict) continue;
      if (!byId.has(id)) byId.set(id, { rows: [], pngs: [] });
      const e = byId.get(id);
      e.rows.push({ verdict, text: text ?? '' });
      const p = png?.trim().replace(/^out\//, '');
      if (p && !e.pngs.includes(p) && has(p)) e.pngs.push(p);
    }
    for (const [id, e] of byId) {
      items.push({
        kind: 'smoke',
        id,
        verdict: worstVerdict(e.rows.map((r) => r.verdict).filter((v) => ['PASS', 'WARN', 'FAIL'].includes(v))),
        rows: e.rows,
        clip: has(`smoke/${id}.mp4`) ? `smoke/${id}.mp4` : null,
        pngs: e.pngs,
        durationS: null,
      });
    }
  }
  if (verbsTsv) {
    for (const line of readFileSync(verbsTsv, 'utf8').split('\n')) {
      if (line.startsWith('#')) {
        parseHeader(line);
        continue;
      }
      const [n, id, verdict, text] = line.split('\t');
      if (!/^\d+$/.test(n ?? '') || !id) continue;
      const stem = `selftest/verbs-${n}-${id}`;
      items.push({
        kind: 'verb',
        id,
        n: Number(n),
        verdict,
        text: text ?? '',
        clip: has(`${stem}.mp4`) ? `${stem}.mp4` : null,
        pngs: has(`${stem}.png`) ? [`${stem}.png`] : [],
        durationS: null,
        editor: id.split('_')[0],
      });
    }
  }
  const shaFile = files.find((f) => f.endsWith(`${sep}build${sep}SHA`));
  if (shaFile) run.sha ??= readFileSync(shaFile, 'utf8').trim() || null;
  return { manifest: { schema: 1, set: key, synthesized: true, run, items }, base };
}

/** Build a gestures manifest from the harness's <suite>/results.json files. */
function synthGestures(root, files) {
  const results = files.filter((f) => f.endsWith(`${sep}results.json`));
  if (!results.length) return null;
  const base = dirname(dirname(results[0]));
  const items = [];
  for (const f of results) {
    let j;
    try {
      j = JSON.parse(readFileSync(f, 'utf8'));
    } catch {
      continue;
    }
    const suite = j.suite ?? rel(base, dirname(f));
    const dir = rel(base, dirname(f));
    for (const r of j.results ?? []) {
      const clip = r.clip ? `${dir}/${r.clip}` : null;
      const slug = r.clip ? r.clip.replace(/\.mp4$/, '') : String(r.n ?? items.length + 1).padStart(2, '0');
      items.push({
        kind: 'gesture',
        id: `${suite}/${slug}`,
        suite,
        test: r.name ?? '',
        verdict: r.passed ? 'PASS' : 'FAIL',
        clip: clip && existsSync(join(base, clip)) ? clip : null,
        durationS: r.durationS ?? null,
      });
    }
  }
  return { manifest: { schema: 1, set: 'gestures', synthesized: true, run: {}, items }, base };
}

function normaliseVerdict(v) {
  const u = String(v ?? '').toUpperCase();
  return u || 'SKIP';
}

/** Find manifest.json (shallowest) or synthesise one; resolve paths against its directory. */
function loadManifest(key, root) {
  const files = walk(root);
  const found = files
    .filter((f) => f.endsWith(`${sep}manifest.json`))
    .sort((a, b) => a.split(sep).length - b.split(sep).length)[0];
  if (found) {
    try {
      return { manifest: JSON.parse(readFileSync(found, 'utf8')), base: dirname(found) };
    } catch (e) {
      log(`${key}: manifest.json is not valid JSON (${e.message}); synthesising instead`);
    }
  }
  return key === 'gestures' ? synthGestures(root, files) : synthRig(key, root, files);
}

/** Copy clips first, then PNGs, under the per-set cap; drop references to anything not copied. */
function bake(key, manifest, base) {
  const dest = join(PUBLIC_DIR, key);
  rmSync(dest, { recursive: true, force: true });
  mkdirSync(dest, { recursive: true });
  let total = 0;
  const skipped = [];
  const copy = (p) => {
    if (!p || p.includes('..')) return false;
    const src = join(base, p);
    if (!existsSync(src)) return false;
    const size = statSync(src).size;
    if (total + size > CAP_BYTES) {
      skipped.push(p);
      return false;
    }
    mkdirSync(dirname(join(dest, p)), { recursive: true });
    copyFileSync(src, join(dest, p));
    total += size;
    return true;
  };
  const copied = new Set();
  const once = (p) => (copied.has(p) ? true : copy(p) && copied.add(p) && true);
  for (const it of manifest.items) if (it.clip && !once(it.clip)) it.clip = null;
  for (const it of manifest.items) it.pngs = (it.pngs ?? []).filter((p) => /\.png$/i.test(p) && once(p));
  writeFileSync(join(dest, 'manifest.json'), JSON.stringify(manifest, null, 2));
  log(`${key}: baked ${copied.size} file(s), ${(total / 1048576).toFixed(1)} MB` + (skipped.length ? `; skipped ${skipped.length} over the ${CAP_BYTES / 1048576} MB cap (${skipped.slice(0, 5).join(', ')}${skipped.length > 5 ? ', …' : ''})` : ''));
}

async function fetchSet({ key, workflow, match }) {
  const run = await latestGreenRun(workflow);
  if (!run) return { status: 'none', why: `no green ${workflow} run on main` };
  const arts = await gh(`/repos/${REPO}/actions/runs/${run.id}/artifacts?per_page=100`);
  const art = (arts?.artifacts ?? []).find((a) => match(a.name) && !a.expired);
  if (!art) return { status: 'none', why: `run ${run.id} has no unexpired artifact for ${key}` };
  const tmp = mkdtempSync(join(tmpdir(), `verified-${key}-`));
  try {
    const zip = join(tmp, 'a.zip');
    const res = await gh(`/repos/${REPO}/actions/artifacts/${art.id}/zip`, { raw: true });
    if (!res) throw new Error(`artifact ${art.id} download 404`);
    await pipeline(Readable.fromWeb(res.body), createWriteStream(zip));
    const root = join(tmp, 'x');
    mkdirSync(root);
    unzip(zip, root);
    const loaded = loadManifest(key, root);
    if (!loaded) return { status: 'none', why: `${art.name} has no manifest.json and nothing to synthesise one from` };
    const { manifest, base } = loaded;
    manifest.set = key;
    manifest.items = (manifest.items ?? []).map((i) => ({ ...i, set: key, verdict: normaliseVerdict(i.verdict) }));
    // Fill what the manifest does not say from the run itself.
    manifest.run = {
      ...manifest.run,
      sha: manifest.run?.sha || run.head_sha,
      date: manifest.run?.date || run.run_started_at || run.created_at,
      runId: manifest.run?.runId && manifest.run.runId !== 'local' ? manifest.run.runId : String(run.id),
      runUrl: run.html_url,
      artifact: art.name,
    };
    bake(key, manifest, base);
    return { status: 'ok', manifest };
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

// --------------------------------------------------------------------- main

function readExisting() {
  try {
    return JSON.parse(readFileSync(DATA_FILE, 'utf8'));
  } catch {
    return null;
  }
}

function writeData(sets, rows, note) {
  const data = {
    generatedAt: new Date().toISOString(),
    repository: REPO,
    note,
    sets,
    rows: joinRows(rows, sets),
  };
  mkdirSync(dirname(DATA_FILE), { recursive: true });
  writeFileSync(DATA_FILE, JSON.stringify(data, null, 1));
  return data;
}

async function main() {
  let rows = [];
  try {
    rows = parseInventory(readFileSync(INVENTORY, 'utf8'));
  } catch (e) {
    log(`inventory not readable (${e.code ?? e.message}); pages will list no rows`);
  }
  const existing = readExisting();
  const empty = { nightly: null, demo: null, gestures: null };

  if (!TOKEN) {
    if (existing) {
      log(`no GITHUB_TOKEN — keeping existing verified data from ${existing.generatedAt}`);
      return;
    }
    writeData(empty, rows, 'no-token');
    log('no GITHUB_TOKEN — wrote empty verified data (pages say "no verified run yet")');
    return;
  }

  const results = await Promise.all(
    SETS.map((s) =>
      fetchSet(s).catch((e) => ({ status: 'error', why: e.name === 'TimeoutError' ? 'timed out' : e.message })),
    ),
  );
  if (existing && results.every((r) => r.status === 'error')) {
    log(`GitHub API failed for every set (${results[0].why}) — keeping existing verified data from ${existing.generatedAt}`);
    return;
  }
  const sets = { ...empty };
  SETS.forEach((s, i) => {
    const r = results[i];
    if (r.status === 'ok') {
      sets[s.key] = r.manifest;
      const m = r.manifest;
      log(`${s.key}: run ${m.run.runId} (${m.run.sha?.slice(0, 7)}, ${m.run.date}) — ${m.items.length} item(s)${m.synthesized ? ' [synthesised: no manifest.json]' : ''}`);
    } else if (r.status === 'error' && existing?.sets?.[s.key]) {
      sets[s.key] = existing.sets[s.key];
      log(`${s.key}: ${r.why} — keeping the previous set from ${existing.generatedAt}`);
    } else {
      rmSync(join(PUBLIC_DIR, s.key), { recursive: true, force: true });
      log(`${s.key}: ${r.why} — no verified run`);
    }
  });
  const data = writeData(sets, rows, 'fetched');
  const linked = data.rows.filter((r) => r.verbs.length + r.smokes.length + r.gestures.length).length;
  log(`wrote ${relative(WEB, DATA_FILE)}: ${data.rows.length} inventory rows, ${linked} with evidence`);
}

main().catch((e) => {
  // Never fail the build over evidence.
  log(`skipped: ${e.message}`);
  if (!readExisting()) {
    try {
      writeData({ nightly: null, demo: null, gestures: null }, [], 'error');
    } catch {}
  }
});
