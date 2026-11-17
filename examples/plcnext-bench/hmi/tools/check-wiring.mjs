// Check the wiring in src/lib/wiring.ts without drawing it: every end seated in its terminal, no wire through a
// housing, the rail, the board, a foot or the desk.     node tools/check-wiring.mjs   (Node 23.6+, which runs .ts)
import { WIRES, BODIES, checkWiring } from '../src/lib/wiring.ts';

const problems = checkWiring();
const samples = WIRES.reduce((n, w) => n + w.path.length, 0);
console.log(`${WIRES.length} wires, ${samples} centreline samples, against ${BODIES.length} bodies and the desk`);
for (const w of WIRES) console.log(`  ${w.name}: ${w.from.name} -> ${w.to ? w.to.name : 'along the desk'}`);
if (problems.length) {
	for (const p of problems) console.log('FAIL', p);
	process.exit(1);
}
console.log('ok: every end is seated in its terminal and nothing passes through anything');
