// Fetch the large assets the rig scene refers to (the HDRI, the backdrop,
// the floor textures) into static/, from the GitHub release assets.json
// names. They are CC0 files of a few MB each and do not belong in git;
// what is committed is this manifest with a size and a sha256 per file,
// so a fetch is verified and a re-run is a no-op.
//
//   npm run assets          # or: node fetch-assets.mjs
//   node fetch-assets.mjs --check   # only report what is missing (CI, naut check)
//
// `npm run dev` and `npm run build` run it first (predev / prebuild).
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

const here = dirname(new URL(import.meta.url).pathname);
const manifest = JSON.parse(readFileSync(join(here, 'assets.json'), 'utf8'));
const checkOnly = process.argv.includes('--check');

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');
const present = (file) => {
	const p = join(here, 'static', file.path);
	return existsSync(p) && statSync(p).size === file.bytes && sha256(readFileSync(p)) === file.sha256;
};

let missing = 0;
for (const file of manifest.files) {
	const p = join(here, 'static', file.path);
	if (present(file)) continue;
	if (checkOnly) {
		console.log(`missing: static/${file.path}`);
		missing++;
		continue;
	}
	process.stdout.write(`fetching ${file.path} (${(file.bytes / 1e6).toFixed(1)} MB) … `);
	const res = await fetch(file.url, { redirect: 'follow' });
	if (!res.ok) {
		console.log(`HTTP ${res.status}`);
		missing++;
		continue;
	}
	const buf = Buffer.from(await res.arrayBuffer());
	if (sha256(buf) !== file.sha256) {
		console.log('sha256 mismatch — not written');
		missing++;
		continue;
	}
	mkdirSync(dirname(p), { recursive: true });
	writeFileSync(p, buf);
	console.log('ok');
}
if (missing) {
	console.error(`${missing} asset(s) missing${checkOnly ? ' — run: npm run assets' : ''}`);
	process.exit(1);
}
if (!checkOnly) console.log(`assets present: ${manifest.files.length} (${manifest.release})`);
