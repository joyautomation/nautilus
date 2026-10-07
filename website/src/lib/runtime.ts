// Reads src/data/runtime.json (written by scripts/fetch-verified.mjs from
// ci.yml's runtime-evidence artifact, which tools/evidence builds) for the
// /verified/runtime/ pages. Generated and gitignored: a missing file means
// "no verified run yet", never a build error.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export type Verdict = 'verified' | 'partial' | 'failing' | 'unrun' | 'gap';

export interface RefResult {
  ref: string;
  status: 'pass' | 'fail' | 'skip' | 'missing';
  passed: number;
  failed: number;
  skipped: number;
}

export interface Claim {
  id: string;
  file: string;
  claim: string;
  source: string;
  partial?: boolean;
  note?: string;
  verdict: Verdict;
  refs: RefResult[];
}

export interface Page {
  file: string;
  page: string;
  title: string;
  prefix: string;
}

export interface PackageStat {
  path: string;
  status: string;
  tests: number;
  subtests: number;
  passed: number;
  failed: number;
  skipped: number;
  statements: number;
  covered: number;
}

export interface ConformanceRow {
  feature: string;
  langs: string[];
  tests: number;
  passed: number;
  failed: number;
}

export interface Runtime {
  schema: number;
  run: { sha?: string | null; date?: string | null; runId?: string; runUrl?: string; local?: boolean };
  totals: {
    packages: number;
    tests: number;
    subtests: number;
    passed: number;
    failed: number;
    skipped: number;
    statements: number;
    covered: number;
  };
  packages: PackageStat[];
  conformance: ConformanceRow[];
  pages: Page[];
  claims: Claim[];
  verdicts: Partial<Record<Verdict, number>>;
  naut?: Record<string, { passed: number; failed: number }>;
}

let cached: Runtime | null | undefined;
export function loadRuntime(): Runtime | null {
  if (cached !== undefined) return cached;
  try {
    cached = JSON.parse(readFileSync(join(process.cwd(), 'src', 'data', 'runtime.json'), 'utf8')) as Runtime;
  } catch {
    cached = null;
  }
  return cached;
}

export const VERDICTS: { key: Verdict; label: string; variant: 'success' | 'tip' | 'danger' | 'note' | 'caution' }[] = [
  { key: 'verified', label: 'verified', variant: 'success' },
  { key: 'partial', label: 'partly verified', variant: 'tip' },
  { key: 'failing', label: 'failing', variant: 'danger' },
  { key: 'unrun', label: 'not run in CI', variant: 'note' },
  { key: 'gap', label: 'no test yet', variant: 'caution' },
];
export const verdictMeta = (v: Verdict) => VERDICTS.find((x) => x.key === v)!;

export const STATUS_VARIANT: Record<RefResult['status'], 'success' | 'danger' | 'note' | 'caution'> = {
  pass: 'success',
  fail: 'danger',
  skip: 'note',
  missing: 'caution',
};

/** The /verified/runtime/<slug>/ page for a claims file. */
export const pageSlug = (p: Page) => p.file.replace(/\.yaml$/, '');

/** Where a documentation page renders on this site. */
export function docsUrl(path: string): string {
  if (path === 'docs/functions.md') return '/reference/functions/';
  if (path === 'docs/testing.md') return '/reference/testing/';
  const m = path.match(/^website\/src\/content\/docs\/(.+?)(?:\/index)?\.mdx?$/);
  return m ? `/${m[1]}/` : `https://github.com/joyautomation/nautilus/blob/main/${path}`;
}

/** A claim's source: "#anchor" on its own page, or "<path>#anchor" elsewhere. */
export function sourceUrl(page: Page, source: string): string {
  const [path, anchor] = source.startsWith('#') ? [page.page, source.slice(1)] : source.split('#');
  return docsUrl(path) + (anchor ? `#${anchor}` : '');
}

export const GROUPS = [
  { key: 'languages', label: 'Languages and functions' },
  { key: 'runtime', label: 'Runtime' },
  { key: 'drivers', label: 'Drivers and protocols' },
  { key: 'logix', label: 'Logix and migrating' },
] as const;

export function groupOf(p: Page): (typeof GROUPS)[number]['key'] {
  if (p.page.includes('/languages/') || p.page === 'docs/functions.md') return 'languages';
  if (p.page.includes('/coming-from/') || /\/logix[^/]*\.md$/.test(p.page)) return 'logix';
  if (/\/(modbus|ethernet-ip|sparkplug|sparkplug-host|it-hardware)\.md$/.test(p.page)) return 'drivers';
  return 'runtime';
}

/** A Go reference's package on GitHub, for linking a test to its source. */
export function refUrl(repo: string, ref: string): string | null {
  const m = ref.match(/^(go|naut) (\S+) /);
  if (!m) return null;
  return `https://github.com/${repo}/tree/main/${m[2].replace(/^\.\//, '')}`;
}

export const pct = (n: number, d: number) => (d ? Math.round((1000 * n) / d) / 10 : 0);
