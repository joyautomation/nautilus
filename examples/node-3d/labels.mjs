// The printed codes for one node, at the sizes and payloads its chassis
// profile's `anchors` give (design doc §3e): the bench label on the lid and
// the racked pair across two empty bay blanks. Print at 100 %.
//
//   node labels.mjs [NODE1] > labels/node1.html
//   HMI_URL=https://mira1.tail913f1.ts.net:9446 node labels.mjs NODE1 > labels/node1.html
//
// `url` anchors encode `${HMI_URL}/a/{node}`, the phone-AR asset link any
// camera app opens; short payloads (`NAUT:{node}/L`) fit the smallest QR
// version, which is what makes a 34 mm code readable at arm's length.
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';

const require = createRequire(new URL('./hmi/package.json', import.meta.url));
const QRCode = require('qrcode');
const profile = JSON.parse(readFileSync(new URL('../../hmi-3d/profiles/supermicro-sys-112b-wr.json', import.meta.url), 'utf8'));
const node = process.argv[2] ?? 'NODE1';
const HMI = (process.env.HMI_URL ?? 'https://mira1.tail913f1.ts.net:9446').replace(/\/$/, '');

const cards = [];
for (const [id, a] of Object.entries(profile.anchors ?? {})) {
	const text = (a.payload ?? 'url') === 'url' ? `${HMI}/a/${node}` : a.payload.replaceAll('{node}', node);
	// Margin 1 module: the size is the code's printed edge, quiet zone
	// included, so it fits the surface the profile measured.
	const qr = QRCode.create(text, { errorCorrectionLevel: 'M' });
	const n = qr.modules.size + 2;
	const svg = await QRCode.toString(text, { errorCorrectionLevel: 'M', margin: 1, type: 'svg' });
	cards.push(`
	<div class="card">
		<div class="qr" style="width:${a.size}mm;height:${a.size}mm">${svg}</div>
		<div class="text">
			<div class="id">${node} · ${id}</div>
			<div class="dim">${a.size} mm · ${qr.modules.size}×${qr.modules.size} (v${qr.version}) · ${(a.size / n).toFixed(2)} mm modules</div>
			<div class="payload">${text}</div>
			<div class="note">${a.note ?? ''}</div>
		</div>
	</div>`);
}

process.stdout.write(`<!doctype html>
<meta charset="utf-8" />
<title>${node} · AR codes</title>
<style>
	@page { size: A4 portrait; margin: 12mm; }
	body { font: 3.2mm/1.35 system-ui, sans-serif; color: #111; margin: 0; }
	h1 { font-size: 5mm; margin: 0 0 2mm; }
	.lead { color: #555; margin: 0 0 6mm; max-width: 170mm; }
	.card { display: grid; grid-template-columns: auto 1fr; gap: 6mm; align-items: center; border: 0.3mm dashed #999;
	        padding: 4mm; margin-bottom: 6mm; break-inside: avoid; width: fit-content; max-width: 180mm; }
	.qr svg { width: 100%; height: 100%; display: block; }
	.id { font-weight: 800; font-size: 4.5mm; }
	.dim, .payload { font-family: ui-monospace, Menlo, monospace; color: #444; }
	.note { color: #555; margin-top: 1.5mm; max-width: 110mm; }
</style>
<h1>${node} (${profile.name}) · AR registration codes</h1>
<p class="lead">Print at 100 % (no "fit to page"), on matte label stock — gloss glare kills the decode. Cut on the code's own edge: each printed size includes a one-module white border and is what the profile's anchor says. Check one edge with a ruler before sticking.</p>
${cards.join('\n')}
`);
