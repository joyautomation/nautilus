// Copies the playground (../playground/dist, from playground/scripts/build.sh)
// into public/playground/engine/, which /playground/ frames. CI builds it
// before the docs (.github/workflows/docs.yml); locally, run the build once.
// Without it the site still builds, and /playground/ says the playground is
// unavailable.
import { cpSync, existsSync, rmSync } from 'node:fs';

const src = new URL('../../playground/dist/', import.meta.url);
const dest = new URL('../public/playground/engine/', import.meta.url);

rmSync(dest, { recursive: true, force: true });
if (existsSync(new URL('naut.wasm', src)) && existsSync(new URL('engine.json', src))) {
  cpSync(src, dest, { recursive: true });
  console.log('sync-playground: playground/dist -> public/playground/engine');
} else {
  console.warn('sync-playground: no playground build (run playground/scripts/build.sh); /playground/ will say it is unavailable');
}
