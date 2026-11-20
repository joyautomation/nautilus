// Bundles the interactive lessons (website/lessons/<id>/) for the tutorial
// page (src/pages/learn/interactive/[id].astro): one JSON per lesson in
// public/learn/lessons/, holding the start project, the solution and the
// hidden check tests, plus <id>.start.json, the start project in the form the
// playground's ?project= takes. A lesson folder is
//   lesson.md   frontmatter (title, track, next, carry, open) + the text
//   start/      what the learner begins with
//   solution/   what Solve loads
//   check/      hidden *_test.yaml files; Check copies them in, runs them
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const lessons = join(root, 'lessons');
const out = join(root, 'public', 'learn', 'lessons');

function tree(dir, base = dir, into = {}) {
  if (!existsSync(dir)) return into;
  for (const name of readdirSync(dir).sort()) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) tree(p, base, into);
    else into[relative(base, p).split('\\').join('/')] = readFileSync(p, 'utf8');
  }
  return into;
}

/** The frontmatter keys the shell needs: scalars and [a, b] lists. */
function front(text) {
  const m = text.match(/^---\n([\s\S]*?)\n---\n/);
  if (!m) throw new Error('lesson.md has no frontmatter');
  const meta = {};
  for (const line of m[1].split('\n')) {
    const kv = line.match(/^(\w+):\s*(.*)$/);
    if (!kv) continue;
    const v = kv[2].trim();
    meta[kv[1]] = v.startsWith('[')
      ? v.slice(1, -1).split(',').map((s) => s.trim().replace(/^["']|["']$/g, '')).filter(Boolean)
      : v.replace(/^["']|["']$/g, '');
  }
  return meta;
}

rmSync(out, { recursive: true, force: true });
if (existsSync(lessons)) {
  mkdirSync(out, { recursive: true });
  const ids = readdirSync(lessons).filter((d) => existsSync(join(lessons, d, 'lesson.md')));
  for (const id of ids) {
    const dir = join(lessons, id);
    const meta = front(readFileSync(join(dir, 'lesson.md'), 'utf8'));
    const start = tree(join(dir, 'start'));
    const solution = tree(join(dir, 'solution'));
    const check = tree(join(dir, 'check'));
    for (const [what, files] of [['start', start], ['solution', solution]]) {
      if (!('nautilus.yaml' in files)) throw new Error(`lesson ${id}: ${what}/ has no nautilus.yaml`);
    }
    if (!Object.keys(check).length) throw new Error(`lesson ${id}: check/ has no tests`);
    const next = meta.next && meta.next !== 'none' ? meta.next : null;
    if (next && !ids.includes(next)) throw new Error(`lesson ${id}: next: ${next} is not a lesson`);
    // Files Next brings over from the learner's previous step (this lesson's
    // carry:); every other file comes from start/, the manifest included.
    const carry = meta.carry ?? [];
    const open = meta.open?.length ? meta.open : ['nautilus.yaml'];
    writeFileSync(join(out, `${id}.json`), JSON.stringify({ id, title: meta.title, next, carry, open, start, solution, check }));
    writeFileSync(join(out, `${id}.start.json`), JSON.stringify({ title: meta.title, open, files: start }));
  }
  console.log(`sync-lessons: ${ids.length} lesson(s) -> public/learn/lessons`);
}
