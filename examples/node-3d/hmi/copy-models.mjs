// The part library ships in the package (hmi-3d/models/hardware/*.glb);
// the app serves it from static/models/hardware, copied here before dev and
// build so the two never drift. The copy is gitignored.
import { cpSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

const pkg = join(import.meta.dirname, 'node_modules/@joyautomation/nautilus-hmi-3d');
const out = join(import.meta.dirname, 'static/models/hardware');
mkdirSync(out, { recursive: true });
cpSync(join(pkg, 'models/hardware'), out, { recursive: true });
console.log('part library →', out);
