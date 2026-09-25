// Undo / redo / save from inside a diagram webview.
//
// VS Code's webview host page catches every keydown in the iframe, blocks the
// browser default for Ctrl/Cmd+Z/Y/S and replays the key in the workbench.
// What the workbench does with it depends on the surface:
//
//   custom editor   `undo` goes to the editor's document (the .fbd/.ld/.sfc
//                   TextDocument) and Ctrl+S saves it — correct, nothing to do.
//   preview panel   a plain WebviewPanel has no document: `undo` runs the
//                   iframe's own native undo (a no-op on a diagram) and Ctrl+S
//                   has nothing to save. So the preview posts the key to the
//                   extension host, which applies it to the source document.
//
// In both, while a text field in the webview has focus (float editor, palette
// search, vars panel) Ctrl+Z / redo must be the field's own text undo. In a
// custom editor the workbench would otherwise undo the DOCUMENT instead, so
// the key is stopped before the host page's listener sees it and the
// browser's native undo runs.

export type DiagramKeyAction = 'undo' | 'redo' | 'save';

export type KeyLike = {
	key: string;
	ctrlKey: boolean;
	metaKey: boolean;
	shiftKey: boolean;
	altKey: boolean;
};

/** What to do with a keydown:
 * - `forward`: post the action to the host (preview panels only),
 * - `native`: a text field owns it — keep it away from the workbench,
 * - `pass`: not ours; leave it to whatever else listens. */
export type KeyDecision =
	| { kind: 'forward'; action: DiagramKeyAction }
	| { kind: 'native' }
	| { kind: 'pass' };

/** The action a chord means, whatever surface it lands on. */
export function keyAction(ev: KeyLike): DiagramKeyAction | undefined {
	if (!(ev.ctrlKey || ev.metaKey) || ev.altKey) return undefined;
	const k = ev.key.toLowerCase();
	if (k === 'z') return ev.shiftKey ? 'redo' : 'undo';
	if (k === 'y' && !ev.shiftKey) return 'redo';
	if (k === 's' && !ev.shiftKey) return 'save';
	return undefined;
}

/** Decide a keydown. `inTextField`: focus is in an input/textarea/select/
 * contenteditable. `forward`: this webview is a preview panel whose host
 * applies keys to the source document. */
export function decideKey(ev: KeyLike, inTextField: boolean, forward: boolean): KeyDecision {
	const action = keyAction(ev);
	if (!action) return { kind: 'pass' };
	// Save has no native meaning in a field, so it still saves the document.
	if (inTextField && action !== 'save') return { kind: 'native' };
	if (forward) return { kind: 'forward', action };
	return { kind: 'pass' };
}

type ElementLike = { tagName?: string; isContentEditable?: boolean } | null | undefined;

export function isTextField(el: ElementLike): boolean {
	if (!el) return false;
	if (el.isContentEditable) return true;
	const tag = (el.tagName ?? '').toUpperCase();
	return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/** Install the listener. Capture phase on window, registered before the app
 * mounts, so it runs ahead of both the app's handlers and the host page's
 * bubble-phase listener. The host opts a webview into forwarding with
 * `<body data-forward-keys="1">`. */
export function installKeyForward(post: (msg: unknown) => void): void {
	const forward = document.body?.dataset.forwardKeys === '1';
	window.addEventListener(
		'keydown',
		(ev) => {
			const d = decideKey(ev, isTextField(document.activeElement), forward);
			if (d.kind === 'pass') return;
			// Either way the workbench must not act on it too: in a field the
			// browser's own undo runs (not prevented); forwarded keys are the
			// host's to apply.
			ev.stopPropagation();
			if (d.kind === 'forward') {
				ev.preventDefault();
				post({ type: 'diagramKey', action: d.action });
			}
		},
		true
	);
}
