// The panel's persisted webview state (vscode.setState — survives a hidden
// tab being torn down and a window reload, never written to the source).
// One object, merged on every save, so independent concerns — the last
// model message, the ladder/SFC zoom — never overwrite each other.
import { vscode } from './vscodeApi';

export type ViewState = {
	/** The last model/diff message, re-shown on reload before the host's
	 * ready-replay lands. */
	msg?: unknown;
	/** Per-editor zoom. Absent = never zoomed: the first load fits. */
	zoom?: { ld?: number; sfc?: number };
};

export function loadViewState(): ViewState {
	const s = vscode.getState() as (ViewState & { type?: unknown }) | null;
	if (!s || typeof s !== 'object') return {};
	// Before zoom existed the state WAS the model message.
	if (typeof s.type === 'string') return { msg: s };
	return s;
}

export function saveViewState(patch: Partial<ViewState>): void {
	vscode.setState({ ...loadViewState(), ...patch });
}
