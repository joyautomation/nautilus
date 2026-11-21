// Bundles the error reporter (src/errors/client.js + the Sentry browser SDK)
// into public/errors/errors.js, which the docs pages and the playground load
// (#1073). The DSN and the release are baked in at build time:
//
//   NAUT_ERRORS_DSN=https://<key>@errors.joyautomation.com/<id> \
//   NAUT_ERRORS_RELEASE=<git sha> node scripts/build-errors.mjs
//
// With NO DSN, nothing is bundled: errors.js is an empty file and nothing is
// sent anywhere (the pages do not even ask for it, see src/lib/errors.mjs).
// Flags, for the tests: --dsn <dsn>, --release <r>, --out <dir>.
import { mkdirSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const args = process.argv.slice(2);
const flag = (name) => (args.includes(name) ? args[args.indexOf(name) + 1] : undefined);
const dsn = flag('--dsn') ?? process.env.NAUT_ERRORS_DSN ?? '';
const release = flag('--release') ?? process.env.NAUT_ERRORS_RELEASE ?? 'unknown';
const out = flag('--out') ?? fileURLToPath(new URL('../public/errors/', import.meta.url));
mkdirSync(out, { recursive: true });
const file = `${out.replace(/\/$/, '')}/errors.js`;

if (!dsn) {
	writeFileSync(file, '/* no error-reporting DSN at build time: nothing is sent */\n');
	console.log('build-errors: no NAUT_ERRORS_DSN; errors.js is empty');
} else {
	if (!/^https?:\/\/[0-9a-f]+@[^/]+\/\d+$/.test(dsn)) {
		console.error('build-errors: NAUT_ERRORS_DSN is not a DSN (https://<key>@<host>/<project id>)');
		process.exit(1);
	}
	await build({
		entryPoints: [fileURLToPath(new URL('../src/errors/client.js', import.meta.url))],
		bundle: true,
		minify: true,
		format: 'iife',
		target: 'es2020',
		outfile: file,
		legalComments: 'none',
		define: { __NAUT_ERRORS_DSN__: JSON.stringify(dsn), __NAUT_ERRORS_RELEASE__: JSON.stringify(release) },
		logLevel: 'warning'
	});
	console.log(`build-errors: ${file} (release ${release})`);
}
