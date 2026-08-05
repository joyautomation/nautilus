// Copies canonical docs out of the repo into the site's content collection,
// prepending the frontmatter Starlight requires. The sources stay the single
// source of truth — the copies under src/content/docs/ are generated and
// gitignored, so a page can never drift from the thing it documents.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));

const pages = [
  {
    src: join(root, '..', 'docs', 'functions.md'),
    dest: join(root, 'src', 'content', 'docs', 'reference', 'functions.md'),
    description:
      'How a scan evaluates each IEC 61131-3 language, and every built-in operator, function, and function block.',
  },
  {
    // The package README is already the reference for the HMI kit; syncing it
    // beats maintaining a second copy that quietly falls behind the components.
    src: join(root, '..', 'hmi', 'README.md'),
    dest: join(root, 'src', 'content', 'docs', 'reference', 'hmi.md'),
    title: 'HMI component kit',
    description:
      'Svelte 5 SCADA faceplates, a realtime SSE client, and a themeable token layer for building operator screens on any nautilus runtime.',
    // Contributor-only; noise for someone reading the docs site.
    dropSections: ['Building the package'],
  },
];

/** Remove whole `## Heading` sections, heading included. */
function dropSections(body, headings) {
  if (!headings?.length) return body;
  const parts = body.split(/\n(?=## )/);
  return parts
    .filter((part) => !headings.some((h) => part.startsWith(`## ${h}`)))
    .join('\n');
}

for (const page of pages) {
  const raw = readFileSync(page.src, 'utf8');
  const lines = raw.split('\n');
  const title = page.title ?? lines[0].replace(/^#\s*/, '').trim();
  const body = dropSections(lines.slice(1).join('\n').trimStart(), page.dropSections);
  const out = `---\ntitle: ${JSON.stringify(title)}\ndescription: ${JSON.stringify(page.description)}\n---\n\n${body}`;
  mkdirSync(dirname(page.dest), { recursive: true });
  writeFileSync(page.dest, out);
  console.log(`synced ${page.src} -> ${page.dest}`);
}
