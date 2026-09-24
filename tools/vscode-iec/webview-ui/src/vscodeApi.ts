// The webview↔extension seam, with a harness fallback: outside VS Code,
// posted messages accumulate on window.__POSTED__ so headless tests can
// assert the exact ops a gesture produces.

type VsCodeApi = {
	postMessage(msg: unknown): void;
	getState(): unknown;
	setState(state: unknown): void;
};

declare global {
	interface Window {
		acquireVsCodeApi?: () => VsCodeApi;
		__POSTED__?: unknown[];
		__MODEL__?: unknown;
	}
}

export const vscode: VsCodeApi = window.acquireVsCodeApi
	? window.acquireVsCodeApi()
	: {
			postMessage: (msg) => {
				(window.__POSTED__ ??= []).push(msg);
			},
			getState: () => null,
			setState: () => {},
		};

export type FbdEditOp = {
	type:
		| 'setLiteral'
		| 'toggleNot'
		| 'rewire'
		| 'rename'
		| 'deleteNode'
		| 'insertStatement'
		| 'setLayout'
		| 'clearLayout'
		| 'disconnect'
		| 'addInput'
		| 'declareVar'
		| 'deleteVar'
		| 'setComment'
		| 'duplicate'
		| 'retarget'
		| 'init';
	node?: string;
	to?: string;
	toPin?: string;
	from?: string;
	fromPin?: string;
	value?: string;
	newName?: string;
	source?: string;
	sourcePin?: string;
	text?: string;
	x?: number;
	y?: number;
	entries?: { node: string; x: number; y: number }[];
	nodes?: string[];
	pou?: string;
};

// A blank (0-byte) file has no POU yet: every op then carries the PROGRAM
// name to seed it with (Go's ApplyEdit writes the skeleton + the op in one
// edit). Set from the model's `blank` flag; undefined otherwise.
let seedPou: string | undefined;
export function setSeedPou(pou: string | undefined): void {
	seedPou = pou;
}
export function withSeed<T extends object>(op: T): T & { pou?: string } {
	return seedPou ? { ...op, pou: seedPou } : op;
}

/** The PROGRAM name a blank file seeds with: its base name, as an IEC
 * identifier (`heater-2.fbd` → `heater_2`). */
export function pouFromFile(file: string | undefined): string {
	const base = (file ?? '').replace(/^.*[\\/]/, '').replace(/\.[^.]*$/, '');
	if (!base) return 'Main';
	let id = base.replace(/[^A-Za-z0-9_]/g, '_');
	if (!/^[A-Za-z_]/.test(id)) id = 'P_' + id;
	return /^[A-Za-z_][A-Za-z0-9_]*$/.test(id) ? id : 'Main';
}

export function postOp(op: FbdEditOp): void {
	vscode.postMessage({ type: 'edit', op: withSeed(op) });
}
