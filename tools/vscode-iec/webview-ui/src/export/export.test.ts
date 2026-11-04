/// <reference types="node" />
// The diagram exporter: models in the shape `naut ld|fbd|sfc graph` emits,
// out as SVG and PDF. The properties that matter on paper: nothing of the
// editor leaks in, a rung or a network never splits across a page, every
// page says what it is and which page it is, and the PDF is a PDF.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { diagramFor, exportPdf, exportSheets, exportSvg, liveSnapshot, sheetSvg } from './index.ts';
import { mergeBlocks, POWER, textWidth, wrapText } from './draw.ts';
import { slicePages } from './paginate.ts';
import { pdfString } from './pdf.ts';
import { dedent } from './sfcExport.ts';
import type { LdModel } from '../ladder.ts';
import type { FbdModel } from '../layout.ts';
import { fitLabel, layoutSfc, type SfcModel } from '../sfc.ts';

function ladder(rungs: number): LdModel {
	return {
		name: 'Permissives',
		comments: [{ line: 1, endLine: 1, text: 'interlocks & permissives: a < b' }],
		rungs: Array.from({ length: rungs }, (_, i) => ({
			name: `r${i}`,
			comment: i === 0 ? 'seal-in (* nested *) with a stop' : undefined,
			line: 10 + i * 3,
			endLine: 11 + i * 3,
			elements: [
				{ kind: 'branch', legs: [[{ kind: 'contact', ref: 'Start' }], [{ kind: 'contact', ref: `Run${i}` }]] },
				{ kind: 'contact', ref: 'Stop', neg: true }
			],
			coils: [{ kind: 'coil', ref: `Run${i}` }]
		}))
	} as LdModel;
}

const fbd: FbdModel = {
	name: 'LevelControl',
	nodes: [
		{ id: 'v:Level', kind: 'input', label: 'Level', layer: 0, line: 5 },
		{ id: 'v:SP', kind: 'input', label: 'HighSP', layer: 0, line: 5 },
		{ id: 'b:ge', kind: 'block', label: 'GE', inputs: ['IN1', 'IN2'], outputs: ['OUT'], layer: 1, line: 5 },
		{ id: 'c:Alarm', kind: 'coil', label: 'HighAlarm', layer: 2, line: 5, exec: 1 },
		{ id: 'g:ghost', kind: 'input', label: 'NotYetWired', layer: 0, ghost: true, x: 400, y: 400 },
		{ id: 'n:1', kind: 'comment', label: 'LAH-101: high level', layer: 0, line: 4 }
	],
	edges: [
		{ from: 'v:Level', to: 'b:ge', toPin: 'IN1' },
		{ from: 'v:SP', to: 'b:ge', toPin: 'IN2', negated: true },
		{ from: 'b:ge', fromPin: 'OUT', to: 'c:Alarm' }
	]
};

const sfc: SfcModel = {
	name: 'PumpSequence',
	steps: [
		{ id: 'st:Idle', name: 'Idle', initial: true, actions: [{ qualifier: 'N', target: 'SetIdle', line: 3 }], line: 2, endLine: 4 },
		{ id: 'st:Run', name: 'Run', initial: false, actions: [{ qualifier: 'S', target: 'Pump', line: 6 }], line: 5, endLine: 7 }
	],
	trans: [
		{ id: 'tr:go', name: 'go', from: ['Idle'], to: ['Run'], cond: 'Level >= HighSP', kind: 'normal', line: 8, endLine: 9 },
		{ id: 'tr:back', name: 'back', from: ['Run'], to: ['Idle'], cond: 'Level < LowSP', kind: 'normal', line: 10, endLine: 11 }
	],
	actions: [{ id: 'ac:SetIdle', name: 'SetIdle', body: 'IF Fault THEN\n\t\tState := 0;\n\tEND_IF;', line: 12, endLine: 15 }]
};

const page = { paper: 'letter' as const, orientation: 'auto' as const, source: 'permissives.ld' };

test('ladder SVG: self-contained, the operands and coils, no editor chrome', () => {
	const svg = exportSvg('ld', ladder(3));
	assert.match(svg, /^<\?xml version="1.0" encoding="UTF-8"\?>\n<svg xmlns="http:\/\/www.w3.org\/2000\/svg"/);
	assert.ok(svg.trimEnd().endsWith('</svg>'));
	for (const label of ['Permissives', 'Ladder Diagram', 'Start', 'Stop', 'Run2', 'r1', 'r0 (* seal-in (* nested *) with a stop *)']) assert.ok(svg.includes(`>${label}<`), label);
	// Text is escaped, never markup.
	assert.ok(svg.includes('interlocks &amp; permissives: a &lt; b'));
	// No classes, styles or scripts: it looks the same wherever it lands.
	assert.doesNotMatch(svg, /class=|<style|<script|href=/);
	// Without a live snapshot nothing is drawn as powered.
	assert.ok(!svg.includes(POWER));
});

/** Why `xml` is not well-formed XML 1.0, or undefined if it is: only the
 * characters XML allows, entity references that exist, balanced tags and
 * one root element. (No XML parser ships with the test dependencies.) */
function xmlError(xml: string): string | undefined {
	const bad = /[^\t\n\r\x20-퟿-�\u{10000}-\u{10FFFF}]/u.exec(xml);
	if (bad) return `character U+${bad[0].codePointAt(0)!.toString(16).padStart(4, '0')} at ${bad.index}`;
	const amp = /&(?!(?:amp|lt|gt|quot|apos|#\d+|#x[0-9a-fA-F]+);)/.exec(xml);
	if (amp) return `bare & at ${amp.index}`;
	const tok = /<\?[^?]*\?>|<!--[\s\S]*?-->|<\/([\w:.-]+)\s*>|<([\w:.-]+)((?:\s+[\w:.-]+\s*=\s*"[^"<]*")*)\s*(\/?)>|([^<]+)/y;
	const stack: string[] = [];
	let roots = 0;
	for (let at = 0; at < xml.length; ) {
		tok.lastIndex = at;
		const m = tok.exec(xml);
		if (!m) return `cannot parse at ${at}: ${JSON.stringify(xml.slice(at, at + 40))}`;
		at = tok.lastIndex;
		if (m[1]) {
			if (stack.pop() !== m[1]) return `</${m[1]}> closes nothing open`;
		} else if (m[2]) {
			if (stack.length === 0) roots++;
			if (!m[4]) stack.push(m[2]);
		} else if (m[5] && stack.length === 0 && m[5].trim()) return 'text outside the root element';
	}
	if (stack.length) return `<${stack.at(-1)}> never closed`;
	return roots === 1 ? undefined : `${roots} root elements`;
}

test('SVG is well-formed XML even when a comment holds control characters', () => {
	const m = ladder(2);
	m.comments = [{ line: 1, endLine: 1, text: 'pasted\x01from\x08a\x0Bterminal\x1F & <done>' }];
	m.rungs[0].comment = 'bell\x07 and tab\tkept';
	for (const svg of [exportSvg('ld', m), exportSvg('fbd', { ...fbd, nodes: [...fbd.nodes, { id: 'n:2', kind: 'comment', label: 'esc\x1B[0m', layer: 0, line: 3 }] } as FbdModel)]) {
		assert.equal(xmlError(svg), undefined);
		assert.doesNotMatch(svg, /[\x00-\x08\x0B\x0C\x0E-\x1F]/);
	}
	assert.ok(exportSvg('ld', m).includes('pasted from a terminal'));
	// The checker itself rejects what the exporter used to write.
	assert.match(xmlError('<svg><text>a\x01b</text></svg>') ?? '', /U\+0001/);
	assert.equal(xmlError('<?xml version="1.0"?>\n<svg a="1"><g/><text>x &amp; y</text></svg>\n'), undefined);
});

test('a long title is cut with an ellipsis before it reaches the diagram kind', () => {
	const many = Array.from({ length: 30 }, (_, i) => `Block${i}`);
	const lib = { ...ladder(2), name: '', blocks: many.map((name, i) => ({ name, line: i * 10 + 1, endLine: i * 10 + 9, pins: [] })) } as LdModel;
	assert.equal(diagramFor('ld', lib).title, many.join(', '));
	for (const orientation of ['portrait', 'landscape'] as const) {
		for (const s of exportSheets('ld', lib, { ...page, orientation })) {
			const texts = s.chrome.filter((p) => p.t === 'text');
			const title = texts.find((p) => p.text.startsWith('Block0'));
			const kind = texts.find((p) => p.text === 'Ladder Diagram');
			assert.ok(title && title.t === 'text' && kind && kind.t === 'text');
			assert.ok(title.text.endsWith('…'), title.text);
			assert.ok(title.text.length > 20);
			// Title ends left of where the right-anchored kind label starts.
			assert.ok(title.x + textWidth(title.text, title.size) < kind.x - textWidth(kind.text, kind.size), orientation);
		}
	}
	// A short title is left alone.
	assert.equal(exportSheets('ld', ladder(2), page)[0].chrome.find((p) => p.t === 'text')?.text, 'Permissives');
	// The SVG cuts its header the same way; its <title> keeps the whole name.
	const svg = exportSvg('ld', lib);
	assert.ok(svg.includes(`<title>${many.join(', ')} — Ladder Diagram</title>`));
	const head = /<text x="12" y="20"[^>]*>([^<]*)<\/text>/.exec(svg)?.[1] ?? '';
	const width = Number(/<svg[^>]* width="([\d.]+)"/.exec(svg)?.[1]);
	assert.ok(head.endsWith('…'), head);
	assert.ok(12 + textWidth(head, 13) < width - 12 - textWidth('Ladder Diagram', 10));
});

test('a multi-statement assign box prints one statement per line, inside its box', () => {
	const text = "Count := Count + 1; Msg := 'it$'s done; really'; Ready := TRUE; Total := Total + Count";
	const m = {
		name: 'Assigns',
		rungs: [{ name: 'r0', line: 1, elements: [{ kind: 'contact', ref: 'Go' }, { kind: 'assign', text }], coils: [] }]
	} as unknown as LdModel;
	const d = diagramFor('ld', m);
	const box = d.items.find((p) => p.t === 'rect' && p.rx === 4);
	assert.ok(box && box.t === 'rect');
	const lines = d.items.filter((p) => p.t === 'text' && p.y > box.y && p.y < box.y + box.h && p.x > box.x && p.x < box.x + box.w);
	// The `;` inside the string literal (after `$'`) does not split it.
	assert.deepEqual(
		lines.map((p) => p.t === 'text' && p.text),
		['Count := Count + 1;', "Msg := 'it$'s done; really';", 'Ready := TRUE;', 'Total := Total + Count']
	);
	for (let i = 0; i < lines.length; i++) {
		const p = lines[i];
		assert.ok(p.t === 'text');
		if (i > 0) assert.ok(p.y > (lines[i - 1] as { y: number }).y + 10, 'stacked, not on top of each other');
		const w = textWidth(p.text, p.size);
		assert.ok(p.x - w / 2 >= box.x + 2 && p.x + w / 2 <= box.x + box.w - 2, `"${p.text}" runs off the box`);
		assert.ok(p.y - p.size >= box.y && p.y + 3 <= box.y + box.h, `"${p.text}" leaves the box vertically`);
	}
	// …and nothing runs off the SVG or onto the next rung.
	const svg = exportSvg('ld', m);
	const svgW = Number(/<svg[^>]* width="([\d.]+)"/.exec(svg)?.[1]);
	assert.ok(box.x + box.w + 12 <= svgW);
	assert.ok(box.y >= 0 && box.y + box.h <= d.height);
	for (const s of [exportSheets('ld', m, page), exportSheets('ld', m, { ...page, paper: 'a4' })]) assert.equal(s.length, 1);
});

test('SFC: a label over its own left run is cut short of the source stem, as the view cuts it', () => {
	const m: SfcModel = {
		name: 'TankBatch',
		steps: ['Idle', 'Fill', 'Heat', 'Mix', 'Drain', 'Aborted'].map((name, i) => ({ id: `st:${name}`, name, initial: i === 0, line: i * 2 + 1, endLine: i * 2 + 2 })),
		trans: [
			{ id: 'tr:t_start', name: 't_start', from: ['Idle'], to: ['Fill'], cond: 'Start', kind: 'normal', line: 13, endLine: 14 },
			{ id: 'tr:t_abort', name: 't_abort_on_operator_request_or_interlock', from: ['Fill'], to: ['Aborted'], cond: 'AbortRequested OR NOT InterlocksHealthy OR EStopActive', kind: 'alt', line: 15, endLine: 16 },
			{ id: 'tr:t_full', name: 't_full', from: ['Fill'], to: ['Heat', 'Mix'], cond: 'Level >= FillSP', kind: 'simDiverge', line: 17, endLine: 18 },
			{ id: 'tr:t_done', name: 't_done', from: ['Heat', 'Mix'], to: ['Drain'], cond: 'TempC >= HeatSP', kind: 'simConverge', line: 19, endLine: 20 },
			{ id: 'tr:t_empty', name: 't_empty', from: ['Drain'], to: ['Idle'], cond: 'Level <= EmptySP', kind: 'normal', line: 21, endLine: 22 }
		]
	};
	const abort = layoutSfc(m).trans.find((r) => r.t.id === 'tr:t_abort')!;
	assert.ok(abort.condMax !== undefined && abort.nameMax !== undefined, 'the layout limits this label');
	const texts = diagramFor('sfc', m).items.flatMap((p) => (p.t === 'text' ? [p.text] : []));
	assert.ok(texts.includes(fitLabel(m.trans[1].cond, abort.condMax)));
	assert.ok(texts.includes(fitLabel(m.trans[1].name!, abort.nameMax)));
	assert.ok(fitLabel(m.trans[1].cond, abort.condMax).endsWith('…'));
	assert.ok(!texts.includes(m.trans[1].cond), 'the full condition crosses the stem');
});

test('ladder with a live snapshot paints power flow and values', () => {
	const live = liveSnapshot({ Start: true, stop: false, Run0: true });
	const svg = exportSvg('ld', ladder(1), { live });
	assert.ok(svg.includes(POWER));
	assert.ok(svg.includes('>TRUE<'));
});

test('a long ladder paginates between rungs, never through one', () => {
	const model = ladder(40);
	const sheets = exportSheets('ld', model, page);
	assert.ok(sheets.length > 2, `expected several pages, got ${sheets.length}`);
	// Every rung lies wholly inside exactly one page's slice.
	const d = diagramFor('ld', model, (sheets[0].w - 72) / 0.75);
	const rungBlocks = d.blocks.filter((b) => !b.keep);
	assert.equal(rungBlocks.length, 40);
	for (const b of rungBlocks) {
		const holders = sheets.filter((s) => s.view.y0 <= b.y0 + 0.01 && b.y1 <= s.view.y1 + 0.01);
		assert.equal(holders.length, 1, `rung at ${b.y0}–${b.y1} on ${holders.length} pages`);
	}
	// Each page names the routine, the file and its number.
	sheets.forEach((s, i) => {
		const texts = s.chrome.flatMap((p) => (p.t === 'text' ? [p.text] : []));
		assert.ok(texts.includes('Permissives'));
		assert.ok(texts.includes('permissives.ld'));
		assert.ok(texts.includes(`Page ${i + 1} of ${sheets.length}`));
	});
	// Printed no larger than on screen, and filling the page's width.
	for (const s of sheets) assert.ok(s.view.scale <= 0.75 + 1e-9);
});

test('pagination: a comment or heading is never the last thing on a page', () => {
	// Blocks of 100 px on a 350 px page at scale 1; the third is a heading.
	const blocks = [
		{ y0: 0, y1: 100 },
		{ y0: 100, y1: 200 },
		{ y0: 200, y1: 300, keep: true },
		{ y0: 300, y1: 400 }
	];
	const pages = slicePages(blocks, 400, 1, 350);
	assert.equal(pages.length, 2);
	assert.equal(pages[0].y1, 200);
	assert.equal(pages[1].y0, 200);
});

test('pagination: a block taller than a page shrinks onto its own page, or slices if shrinking would be unreadable', () => {
	const shrunk = slicePages([{ y0: 0, y1: 50 }, { y0: 50, y1: 450 }], 450, 1, 350);
	assert.equal(shrunk.length, 2);
	assert.ok(shrunk[1].scale < 1 && shrunk[1].scale >= 0.6);
	const sliced = slicePages([{ y0: 0, y1: 2000 }], 2000, 1, 350);
	assert.ok(sliced.length >= 5);
	assert.ok(sliced.every((p) => p.scale === 1));
	assert.equal(sliced[sliced.length - 1].y1, 2000);
});

test('mergeBlocks joins overlapping spans but keeps touching rungs apart', () => {
	assert.deepEqual(mergeBlocks([{ y0: 0, y1: 10 }, { y0: 10, y1: 20 }]).length, 2);
	assert.deepEqual(mergeBlocks([{ y0: 0, y1: 10 }, { y0: 5, y1: 20 }]), [{ y0: 0, y1: 20, keep: undefined }]);
});

test('FBD: blocks, pins, negation and execution order print; ghosts do not', () => {
	const svg = exportSvg('fbd', fbd);
	for (const label of ['LevelControl', 'Function Block Diagram', 'Level', 'HighSP', 'GE', 'IN1', 'OUT', 'HighAlarm', 'LAH-101: high level']) {
		assert.ok(svg.includes(`>${label}<`), label);
	}
	assert.ok(!svg.includes('NotYetWired'));
	assert.match(svg, /<circle [^>]*r="4.5"/); // the NOT circle at IN2
	assert.ok(svg.includes('>1<')); // execution-order badge
});

test('SFC: steps, conditions, the jump back and the ACTION bodies', () => {
	const svg = exportSvg('sfc', sfc);
	for (const label of ['Idle', 'Run', 'Level &gt;= HighSP', 'Idle: Level &lt; LowSP', 'ACTION SetIdle', 'IF Fault THEN', '    State := 0;', 'END_IF;']) {
		assert.ok(svg.includes(`>${label}<`), label);
	}
	const live = liveSnapshot({ _S_Run_X: true });
	assert.ok(exportSvg('sfc', sfc, { live }).includes(POWER));
});

test('dedent strips the body indent the model keeps after the first line', () => {
	assert.deepEqual(dedent('IF a THEN\n        b := 1;\n    END_IF;\n'), ['IF a THEN', '    b := 1;', 'END_IF;']);
	assert.deepEqual(dedent('x := 1;'), ['x := 1;']);
});

test('wrapText keeps line breaks and splits long words', () => {
	assert.deepEqual(wrapText('one two three\nfour', 9), ['one two', 'three', 'four']);
	assert.deepEqual(wrapText('abcdefghijkl', 8), ['abcdefgh', 'ijkl']);
});

/** The xref table's offsets each land on their object's header. */
function checkPdf(bytes: Uint8Array): { pages: number; text: string } {
	const s = Buffer.from(bytes).toString('latin1');
	assert.ok(s.startsWith('%PDF-1.4\n'));
	assert.ok(s.endsWith('%%EOF\n'));
	const startxref = Number(/startxref\n(\d+)\n%%EOF\n$/.exec(s)![1]);
	assert.ok(s.slice(startxref).startsWith('xref\n'));
	const rows = s.slice(startxref).split('\n').slice(3).filter((l) => / n $/.test(l));
	rows.forEach((row, i) => {
		const off = Number(row.slice(0, 10));
		assert.ok(s.slice(off).startsWith(`${i + 1} 0 obj\n`), `object ${i + 1} at ${off}`);
	});
	// Each content stream's /Length is its byte length.
	for (const m of s.matchAll(/<< \/Length (\d+) >>\nstream\n/g)) {
		const start = m.index! + m[0].length;
		assert.equal(s.slice(start + Number(m[1]), start + Number(m[1]) + 10), '\nendstream');
	}
	return { pages: Number(/\/Type \/Pages \/Kids \[[^\]]*\] \/Count (\d+)/.exec(s)![1]), text: s };
}

test('PDF: a valid file, one page per sheet, page numbers on each', () => {
	const model = ladder(40);
	const sheets = exportSheets('ld', model, page);
	const { pages, text } = checkPdf(exportPdf('ld', model, page));
	assert.equal(pages, sheets.length);
	assert.ok(text.includes(`(Page ${sheets.length} of ${sheets.length}) Tj`));
	assert.ok(text.includes('/BaseFont /Courier '));
	assert.ok(text.includes('/MediaBox [0 0 612 792]'));
	for (const lang of ['fbd', 'sfc'] as const) checkPdf(exportPdf(lang, lang === 'fbd' ? fbd : sfc, { ...page, paper: 'a4' }));
	// The same sheet as an SVG page: chrome, and the slice clipped to the page.
	const svg = sheetSvg(sheets[1], 1);
	assert.ok(svg.includes(`>Page 2 of ${sheets.length}<`));
	assert.match(svg, /<g clip-path="url\(#clip1\)">/);
});

test('a ladder library: FUNCTION_BLOCK headings with their interface, titled by the block', () => {
	const lib = {
		name: '',
		blocks: [
			{
				name: 'MotorStarter',
				line: 1,
				endLine: 20,
				pins: Array.from({ length: 12 }, (_, i) => ({ name: `Input${i}`, type: 'BOOL', dir: i < 10 ? 'in' : 'out' }))
			}
		],
		rungs: [{ name: 'run', line: 5, pou: 'MotorStarter', elements: [{ kind: 'contact', ref: 'Input0' }], coils: [{ kind: 'coil', ref: 'Input10' }] }]
	} as LdModel;
	const d = diagramFor('ld', lib);
	assert.equal(d.title, 'MotorStarter');
	const texts = d.items.flatMap((p) => (p.t === 'text' ? [p.text] : []));
	assert.ok(texts.includes('FUNCTION_BLOCK'));
	// The interface wraps between items, never inside one.
	const pinLines = texts.filter((t) => t.includes(' : BOOL'));
	assert.ok(pinLines.length > 1, 'wrapped');
	for (const l of pinLines) assert.match(l, /^(OUT )?Input\d+ : BOOL(, (OUT )?Input\d+ : BOOL)*,?$/);
});

test('PDF: orientation follows the setting, and auto turns a wide diagram sideways', () => {
	const narrow = exportSheets('ld', ladder(2), page);
	assert.ok(narrow[0].h > narrow[0].w);
	assert.ok(exportSheets('ld', ladder(2), { ...page, orientation: 'landscape' })[0].w > 700);
	const wide: LdModel = {
		name: 'Wide',
		rungs: [{ name: 'w', line: 1, elements: Array.from({ length: 16 }, (_, i) => ({ kind: 'contact', ref: `Input_${i}` })), coils: [{ kind: 'coil', ref: 'Q' }] }]
	} as LdModel;
	const sheet = exportSheets('ld', wide, page)[0];
	assert.ok(sheet.w > sheet.h, 'landscape');
});

test('PDF strings: escaped and in WinAnsi', () => {
	assert.equal(pdfString('a(b)c\\'), '(a\\(b\\)c\\\\)');
	assert.equal(pdfString('x → y …'), '(x -> y \x85)');
	assert.equal(pdfString('°C'), '(\xb0C)');
	assert.equal(pdfString('水'), '(?)');
});
