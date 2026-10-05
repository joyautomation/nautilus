// Reads src/data/verified.json (written by scripts/fetch-verified.mjs at
// prebuild) for the /verified/ pages and the <Clip> component. The file is
// generated and gitignored, so a missing or unreadable file is the same as
// "no verified run yet" — never a build error.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export type SetKey = 'nightly' | 'demo' | 'gestures';

export interface Item {
  kind: 'smoke' | 'verb' | 'gesture';
  id: string;
  set: SetKey;
  verdict: string;
  text?: string;
  rows?: { verdict: string; text: string }[];
  clip?: string | null;
  pngs?: string[];
  durationS?: number | null;
  n?: number;
  editor?: string;
  suite?: string;
  test?: string;
}

export interface Run {
  sha?: string | null;
  naut?: string | null;
  vsix?: string | null;
  vscode?: string | null;
  date?: string | null;
  pace?: string | null;
  frame?: { w: number; h: number; zoom?: number; font?: number } | null;
  runId?: string | null;
  runUrl?: string;
  host?: string | null;
  artifact?: string;
}

export interface Manifest {
  schema: number;
  set: SetKey;
  synthesized?: boolean;
  run: Run;
  items: Item[];
}

export interface Row {
  id: string;
  section: string;
  area: string;
  gesture: string;
  source: string;
  webviewTest: string;
  rigVerb: string;
  smoke: string;
  refs: { verbs: string[]; smokes: string[] };
  verbs: Item[];
  smokes: Item[];
  gestures: Item[];
}

export interface Verified {
  generatedAt: string | null;
  repository: string;
  sets: Record<SetKey, Manifest | null>;
  rows: Row[];
}

const EMPTY: Verified = {
  generatedAt: null,
  repository: 'joyautomation/nautilus',
  sets: { nightly: null, demo: null, gestures: null },
  rows: [],
};

let cached: Verified | undefined;
export function loadVerified(): Verified {
  if (cached) return cached;
  try {
    const d = JSON.parse(readFileSync(join(process.cwd(), 'src', 'data', 'verified.json'), 'utf8'));
    cached = { ...EMPTY, ...d, sets: { ...EMPTY.sets, ...(d.sets ?? {}) }, rows: d.rows ?? [] };
  } catch {
    cached = EMPTY;
  }
  return cached;
}

export const SECTIONS = [
  { slug: 'fbd', label: 'FBD editor', blurb: 'The function block diagram editor’s ? list.' },
  { slug: 'ladder', label: 'Ladder editor', blurb: 'The ladder editor’s ? list.' },
  { slug: 'sfc', label: 'SFC editor', blurb: 'The sequential function chart editor’s ? list.' },
  { slug: 'mimic', label: 'Mimic editor', blurb: 'The HMI mimic editor’s ? list.' },
  { slug: 'component', label: 'Component editor', blurb: 'The component (ports) editor’s ? list.' },
  { slug: 'commands', label: 'Commands', blurb: 'Every command the extension contributes.' },
  { slug: 'other', label: 'Other claims', blurb: 'Feature claims from the README, CHANGELOG and menus.' },
] as const;

export const SET_LABEL: Record<SetKey, string> = {
  nightly: 'rig nightly',
  demo: 'rig demo',
  gestures: 'gesture harness',
};

/** URL of a file baked into public/verified/<set>/. */
export const mediaUrl = (set: SetKey, path: string) =>
  `/verified/${set}/${path.split('/').map(encodeURIComponent).join('/')}`;

const PASSING = new Set(['PASS', 'WARN', 'XPASS', 'XFAIL', 'NOTE']);
export const isPassing = (v: string) => PASSING.has(v);

export function badgeVariant(v: string): 'success' | 'caution' | 'danger' | 'default' {
  if (v === 'PASS') return 'success';
  if (v === 'FAIL' || v === 'XPASS') return 'danger';
  if (v === 'WARN' || v === 'XFAIL') return 'caution';
  return 'default';
}

export const rowItems = (r: Row) => [...r.verbs, ...r.smokes, ...r.gestures];

/** One verdict for a row: any FAIL wins, then WARN-ish, then PASS; nothing linked → null. */
export function rowVerdict(r: Row): string | null {
  const vs = rowItems(r).map((i) => i.verdict);
  if (!vs.length) return null;
  for (const v of ['FAIL', 'XPASS', 'WARN', 'XFAIL', 'PASS']) if (vs.includes(v)) return v;
  return vs[0];
}

/** "Covered": at least one linked test passed in the last green runs. */
export const isCovered = (r: Row) => rowItems(r).some((i) => isPassing(i.verdict));

/** The clip to show for a row: demo verb, nightly verb, nightly smoke, then harness. */
export function rowClip(r: Row): { item: Item; harness: boolean } | null {
  // Exact verb names first: `ld_add_contact` before its `ld_add_contact-nc` variant.
  const exact = (i: Item) => (r.refs.verbs.includes(i.id) ? 0 : 1);
  const verbs = [...r.verbs].sort((a, b) => exact(a) - exact(b));
  const order = [
    ...verbs.filter((i) => i.set === 'demo'),
    ...verbs.filter((i) => i.set === 'nightly'),
    ...r.smokes.filter((i) => i.set === 'demo'),
    ...r.smokes.filter((i) => i.set === 'nightly'),
  ];
  const rig = order.find((i) => i.clip);
  if (rig) return { item: rig, harness: false };
  const g = r.gestures.find((i) => i.clip);
  return g ? { item: g, harness: true } : null;
}

export function formatDate(iso?: string | null): string {
  if (!iso) return 'unknown date';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return (
    d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }) +
    ', ' +
    d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' }) +
    ' UTC'
  );
}

export const commitUrl = (repo: string, sha: string) => `https://github.com/${repo}/commit/${sha}`;

/**
 * Find an item by id for <Clip>: exact id in demo, nightly, gestures; then a
 * verb variant (`ld_add_contact` → `ld_add_contact-nc` only if no exact hit),
 * a smoke number (`04` → `04-title-buttons`).
 */
export function findItem(id: string): Item | null {
  const v = loadVerified();
  const pools = (['demo', 'nightly', 'gestures'] as SetKey[]).map((k) => v.sets[k]?.items ?? []);
  for (const pool of pools) {
    const hit = pool.find((i) => i.id === id && i.clip);
    if (hit) return hit;
  }
  for (const pool of pools) {
    const hit = pool.find((i) => i.clip && (i.id.startsWith(`${id}-`) || i.id === id));
    if (hit) return hit;
  }
  return null;
}

/** The inventory row an item is evidence for, for linking back to /verified/<section>/#<row>. */
export function rowFor(item: Item): Row | null {
  const v = loadVerified();
  return (
    v.rows.find((r) => rowItems(r).some((i) => i.set === item.set && i.id === item.id)) ?? null
  );
}

export const rowAnchor = (r: Row) => `/verified/${r.section}/#${r.id.toLowerCase()}`;

/** Item label for lists: "rig verb ld_add_branch", "smoke 04-title-buttons", "harness diagram: …". */
export function itemLabel(i: Item): string {
  if (i.kind === 'verb') return `rig verb ${i.id}`;
  if (i.kind === 'smoke') return `smoke check ${i.id}`;
  return `${i.suite ?? 'harness'}: ${i.test ?? i.id}`;
}
