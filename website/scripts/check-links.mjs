// Checks the built site (dist/) for internal links that go nowhere: every
// href="/..." and src="/..." in every page must name a file or a page that
// exists, and a #fragment must name an id on the page it points to.
//
//   node scripts/check-links.mjs                 # after `astro build`
//   node scripts/check-links.mjs --urls old.txt  # also: every URL listed in
//                                                # old.txt (one per line, as
//                                                # `foo/bar/`) must still exist
//
// External links are not followed. Exit 1 on any problem.
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const args = process.argv.slice(2);
const urlsFile = args[args.indexOf('--urls') + 1];

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
const uniq = [...new Set(bad)];
for (const b of uniq) console.log(b);
console.log(`check-links: ${pages.length} pages, ${uniq.length} problem(s)`);
process.exit(uniq.length ? 1 : 0);
