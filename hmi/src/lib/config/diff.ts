// A unified line diff of the files an edit changed (#711, slice 3), the
// same shape as `naut pull --io --dry-run` (cmd/naut/udiff.go): three lines
// of context, hunks merged when their contexts touch. Pure, no library;
// config files are manifests and profiles, so the O(n*m) match is fine.

type Op = { kind: ' ' | '-' | '+'; text: string };

const lines = (s: string): string[] => (s === '' ? [] : s.replace(/\n$/, '').split('\n'));

/** A unified diff of a to b; '' when they are equal. */
export function unifiedDiff(nameA: string, nameB: string, a: string, b: string): string {
	if (a === b) return '';
	const la = lines(a);
	const lb = lines(b);
	const n = la.length;
	const m = lb.length;
	const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
	for (let i = n - 1; i >= 0; i--)
		for (let j = m - 1; j >= 0; j--) lcs[i][j] = la[i] === lb[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
	const ops: Op[] = [];
	let i = 0;
	let j = 0;
	while (i < n || j < m) {
		if (i < n && j < m && la[i] === lb[j]) ops.push({ kind: ' ', text: la[i++] }), j++;
		else if (i < n && (j === m || lcs[i + 1][j] >= lcs[i][j + 1])) ops.push({ kind: '-', text: la[i++] });
		else ops.push({ kind: '+', text: lb[j++] });
	}
	const ctx = 3;
	let out = `--- ${nameA}\n+++ ${nameB}\n`;
	for (let k = 0; k < ops.length; ) {
		if (ops[k].kind === ' ') {
			k++;
			continue;
		}
		const start = Math.max(k - ctx, 0);
		let end = k;
		while (end < ops.length) {
			if (ops[end].kind !== ' ') {
				end++;
				continue;
			}
			let gap = 0;
			for (let e = end; e < ops.length && ops[e].kind === ' '; e++) gap++;
			if (end + gap < ops.length && gap <= 2 * ctx) {
				end += gap;
				continue;
			}
			break;
		}
		const stop = Math.min(end + ctx, ops.length);
		let aStart = 1;
		let bStart = 1;
		for (const o of ops.slice(0, start)) {
			if (o.kind !== '+') aStart++;
			if (o.kind !== '-') bStart++;
		}
		let aLen = 0;
		let bLen = 0;
		let body = '';
		for (const o of ops.slice(start, stop)) {
			if (o.kind !== '+') aLen++;
			if (o.kind !== '-') bLen++;
			body += `${o.kind}${o.text}\n`;
		}
		if (aLen === 0) aStart--;
		if (bLen === 0) bStart--;
		out += `@@ -${aStart},${aLen} +${bStart},${bLen} @@\n${body}`;
		k = stop;
	}
	return out;
}

/** The diff of every changed file: before is what runs, after the overlay. */
export function diffFiles(before: Record<string, string>, after: Record<string, string>): { path: string; diff: string }[] {
	return Object.keys(after)
		.sort()
		.map((path) => ({ path, diff: unifiedDiff(`a/${path}`, `b/${path}`, before[path] ?? '', after[path]) }))
		.filter((d) => d.diff !== '');
}
