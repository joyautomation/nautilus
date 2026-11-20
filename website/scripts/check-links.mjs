// Checks the built site (dist/) for internal links that go nowhere: every
// href="/..." and src="/..." in every page must name a file or a page that
// exists, and a #fragment must name an id on the page it points to.
//
//   node scripts/check-links.mjs                 # after `astro build`
//   node scripts/check-links.mjs --urls old.txt  # also: every URL listed in
//                                                # old.txt (one per line, as
//                                                # `foo/bar/`) must still exist
//   node scripts/check-links.mjs --sidebar       # also: every docs page must
//                                                # be listed in the sidebar
//
// External links are not followed. Exit 1 on any problem.
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const args = process.argv.slice(2);
const urlsFile = args.includes('--urls') ? args[args.indexOf('--urls') + 1] : undefined;

const pages = [];
(function walk(d) {
  for (const f of readdirSync(d)) {
    const p = join(d, f);
    if (statSync(p).isDirectory()) walk(p);
    else if (f.endsWith('.html')) pages.push(p);
  }
})(dist);

const exists = (url) => {
  const p = join(dist, decodeURIComponent(url));
  return existsSync(p) && (statSync(p).isFile() || existsSync(join(p, 'index.html')));
};
const idsCache = new Map();
const ids = (url) => {
  const p = join(dist, decodeURIComponent(url));
  const file = statSync(p).isDirectory() ? join(p, 'index.html') : p;
  if (!idsCache.has(file)) {
    const set = new Set();
    for (const m of readFileSync(file, 'utf8').matchAll(/\sid="([^"]+)"/g)) set.add(m[1]);
    idsCache.set(file, set);
  }
  return idsCache.get(file);
};

const bad = [];
for (const page of pages) {
  const html = readFileSync(page, 'utf8');
  const here = '/' + page.slice(dist.length + 1).replace(/index\.html$/, '');
  for (const m of html.matchAll(/\s(?:href|src)="([^"]+)"/g)) {
    let u = m[1];
    if (!u.startsWith('/') && !u.startsWith('#')) continue; // external, mailto, relative asset
    if (u.startsWith('//')) continue;
    // The playground's HMI pages link to /playground/engine/view/<name>/_app/…,
    // which no file in dist/ holds: its service worker serves that prefix from
    // hmi-packs.tgz at runtime, so the links work in a browser and can't be
    // checked here.
    if (u.startsWith('/playground/engine/view/')) continue;
    const [path, frag] = u.split('#');
    const target = path === '' ? here : path.split('?')[0];
    if (!exists(target)) { bad.push(`${here}: ${u} (no such page or file)`); continue; }
    if (frag && target.endsWith('/') && !ids(target).has(frag)) bad.push(`${here}: ${u} (no #${frag} on that page)`);
  }
}
if (urlsFile) {
  for (const line of readFileSync(urlsFile, 'utf8').split('\n').filter(Boolean)) {
    if (!exists('/' + line)) bad.push(`old URL /${line} no longer exists (add a redirect)`);
  }
}
if (args.includes('--sidebar')) {
  // The sidebar is the same on every page: read it off the Learn overview.
  const html = readFileSync(join(dist, 'learn', 'index.html'), 'utf8');
  const start = html.indexOf('<ul class="top-level');
  const side = new Set();
  for (const m of html.slice(start, html.indexOf('</nav>', start)).matchAll(/href="(\/[^"#]*)"/g)) side.add(m[1]);
  for (const page of pages) {
    const url = '/' + page.slice(dist.length + 1).replace(/index\.html$/, '');
    // not documentation: the 404, the playground frame, the generated
    // /verified/ evidence pages (their own sidebar group), the interactive
    // lessons (generated from website/lessons/, linked from the Learn
    // overview, so a dead link to one is still caught), assets
    if (url === '/404.html' || url.startsWith('/playground/') || url.startsWith('/verified/') || url.startsWith('/learn/interactive/') || url.startsWith('/_astro/')) continue;
    if (url === '/') continue; // the splash page
    if (!side.has(url)) bad.push(`${url} is not in the sidebar (add it to astro.config.mjs)`);
  }
}
const uniq = [...new Set(bad)];
for (const b of uniq) console.log(b);
console.log(`check-links: ${pages.length} pages, ${uniq.length} problem(s)`);
process.exit(uniq.length ? 1 : 0);
