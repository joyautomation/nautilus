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
		| 'clearLayout';
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
};

export function postOp(op: FbdEditOp): void {
	vscode.postMessage({ type: 'edit', op });
}
