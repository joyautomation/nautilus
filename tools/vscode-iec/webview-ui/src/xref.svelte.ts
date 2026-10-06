// Cross-reference (#218) and tag descriptions (#216) on diagram elements —
// ONE module the ladder, FBD and SFC views share, so each view's part is a
// few attributes and a tooltip suffix, nothing more.
//
// The DOM contract. An element that draws an identifier carries
//
//     data-xref="<name as drawn>"        M1_Run, t1.Q, Levels[i], +M1_StartPB
//     data-xref-line="<1-based line>"    the source lines it came from: a
//     data-xref-end="<1-based line>"     rung's RUNG..last line, a step's block
//
// and this module does the rest, installed once from main.ts:
//
//   - Shift+F12 with such an element selected (or last clicked), or its
//     right-click menu's "Find All References", posts
//     {type:'xref', name, line, endLine}; the extension host
//     (src/diagramXref.ts) finds the name in the diagram's text and opens
//     VS Code's References view on it.
//   - {type:'descriptions', descriptions, show} from the host — every
//     name's description (manifest desc, or the VAR line's comment), lower-
//     cased keys — lands in `descriptions`. Views read it through
//     withDesc() for tooltips and descLine() for the ladder's second line.

import { live } from './liveState.svelte';

export const descriptions = $state({
	/** lower-cased name → description */
	map: {} as Record<string, string>,
	/** nautilus.diagram.showDescriptions: draw the ladder's second line. */
	show: true
});

export function setDescriptions(map: Record<string, string> | undefined, show: boolean | undefined): void {
	const next: Record<string, string> = {};
	for (const [k, v] of Object.entries(map ?? {})) if (typeof v === 'string' && v) next[k.toLowerCase()] = v;
	descriptions.map = next;
	descriptions.show = show !== false;
}

/** The description of a name as an element draws it: exact (any case),
 * then without its index (`Levels[i]` → Levels) or edge mark (`+Start`). */
export function describe(name: string | undefined): string | undefined {
	if (!name) return undefined;
	const m = descriptions.map;
	const exact = m[name.trim().toLowerCase()];
	if (exact) return exact;
	const bare = name
		.trim()
		.replace(/^[+\-/]\s*/, '')
		.replace(/\[[^\]]*\]/g, '')
		.toLowerCase();
	return m[bare];
}

/** A tooltip with the name's description as its last line. */
export function withDesc(title: string, name: string | undefined): string {
	const d = describe(name);
	return d ? `${title}\n${d}` : title;
}

/** The same, as a suffix for a tooltip built from parts in a template. */
export function descTail(name: string | undefined): string {
	const d = describe(name);
	return d ? `\n${d}` : '';
}

/** Average glyph width of the ladder's description line (9.5px sans). */
const DESC_CH = 5.3;

/** The ladder's second line under an operand: the description, fit to the
 * element's width ('' when there is none or the setting is off). */
export function descLine(name: string | undefined, widthPx: number): string {
	if (!descriptions.show) return '';
	const d = describe(name);
	if (!d) return '';
	// Within the element's own width: a wider line runs over a branch's
	// rails or the next element's label.
	const max = Math.max(6, Math.floor((widthPx - 2) / DESC_CH));
	return d.length <= max ? d : d.slice(0, max - 1).trimEnd() + '…';
}

// ── cross-reference ────────────────────────────────────────────────────────

export type XrefMsg = { type: 'xref'; name: string; line?: number; endLine?: number };

type KeyLike = { key: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; altKey: boolean };

/** Shift+F12 — VS Code's Find All References — and nothing else. */
export function isXrefKey(ev: KeyLike): boolean {
	return ev.key === 'F12' && ev.shiftKey && !ev.ctrlKey && !ev.metaKey && !ev.altKey;
}

const num = (s: string | undefined) => {
	const n = s === undefined || s === '' ? NaN : Number(s);
	return Number.isFinite(n) ? n : undefined;
};

/** The xref message for the element at (or above) `el`, if it names one. */
export function xrefOf(el: Element | null | undefined): XrefMsg | undefined {
	const host = el?.closest?.('[data-xref]') as HTMLElement | SVGElement | null | undefined;
	const name = host?.getAttribute('data-xref')?.trim();
	if (!host || !name) return undefined;
	const line = num(host.getAttribute('data-xref-line') ?? undefined);
	const endLine = num(host.getAttribute('data-xref-end') ?? undefined) ?? line;
	return { type: 'xref', name, ...(line !== undefined ? { line, endLine } : {}) };
}

/** What Shift+F12 is about: the one selected element naming an identifier
 * (ladder/SFC mark the element itself `.selected`, Svelte Flow the node
 * wrapping it), else the last element clicked. */
function currentTarget(armed: XrefMsg | undefined): XrefMsg | undefined {
	const sel = [...document.querySelectorAll('[data-xref].selected, .svelte-flow__node.selected [data-xref]')];
	// A selected FBD node's pin spans carry their own data-xref; the node's
	// own element (the outermost) is what's selected.
	const outer = sel.filter((e) => !sel.some((o) => o !== e && o.contains(e)));
	if (outer.length === 1) return xrefOf(outer[0]);
	return armed;
}

const MENU_CSS = `
.nx-xref-menu { position: fixed; z-index: 1000; min-width: 190px; padding: 4px 0;
  background: var(--vscode-menu-background, var(--nx-panel-bg)); color: var(--vscode-menu-foreground, var(--nx-ink));
  border: 1px solid var(--vscode-menu-border, var(--nx-border)); border-radius: 5px; box-shadow: 0 2px 8px var(--nx-shadow, rgba(0,0,0,.35));
  font: 12px var(--nx-font, sans-serif); }
.nx-xref-menu button { display: flex; justify-content: space-between; gap: 24px; width: 100%; padding: 4px 12px; border: 0;
  background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; }
.nx-xref-menu button:hover, .nx-xref-menu button:focus { background: var(--vscode-menu-selectionBackground, var(--nx-sel-bg));
  color: var(--vscode-menu-selectionForeground, var(--nx-sel-ink)); outline: none; }
.nx-xref-menu kbd { font: inherit; opacity: .7; }
text.nx-desc { fill: var(--nx-muted); font-family: var(--nx-font, sans-serif); font-size: 9.5px; font-style: italic; pointer-events: none; }
`;

/** Install once, before the app mounts (main.ts): the key, the right-click
 * menu, and the descriptions message. */
export function installXref(post: (msg: unknown) => void): void {
	const style = document.createElement('style');
	style.textContent = MENU_CSS;
	document.head.appendChild(style);

	// The descriptions message is this module's alone: stop it before the
	// app's own listener (registered after this one) would file it as a view.
	window.addEventListener('message', (ev) => {
		const msg = ev.data as { type?: string; descriptions?: Record<string, string>; show?: boolean } | null;
		if (msg?.type !== 'descriptions') return;
		ev.stopImmediatePropagation();
		setDescriptions(msg.descriptions, msg.show);
	});

	// The element last pressed on, for Shift+F12 where nothing is selected
	// (a read-only ladder, an SFC association row).
	let armed: XrefMsg | undefined;
	window.addEventListener('pointerdown', (ev) => {
		const t = ev.target as Element | null;
		if (t?.closest?.('.nx-xref-menu')) return;
		armed = xrefOf(t);
		closeMenu();
	}, true);

	window.addEventListener('keydown', (ev) => {
		if (ev.key === 'Escape' && menu) {
			closeMenu();
			ev.stopPropagation();
			return;
		}
		if (!isXrefKey(ev)) return;
		const target = currentTarget(armed);
		// Ours either way: the workbench has no editor here to search from.
		ev.preventDefault();
		ev.stopPropagation();
		if (target) post(target);
	}, true);

	let menu: HTMLDivElement | null = null;
	function closeMenu() {
		menu?.remove();
		menu = null;
	}
	window.addEventListener('blur', closeMenu);
	window.addEventListener('contextmenu', (ev) => {
		const target = xrefOf(ev.target as Element | null);
		closeMenu();
		if (!target) return;
		// An element with a native VS Code menu of its own (an SFC step or
		// transition: data-vscode-context, package.json webview/context —
		// Set Active Step, Fire Transition) keeps it while those items are
		// on offer, i.e. while connected to a controller; Shift+F12 still
		// answers there. Offline, that menu would be only Cut/Copy/Paste,
		// so ours shows instead.
		if ((ev.target as Element | null)?.closest?.('[data-vscode-context]') && live.enabled && live.fresh) return;
		// Our menu instead of the webview's Cut/Copy/Paste one.
		ev.preventDefault();
		ev.stopPropagation();
		armed = target;
		menu = document.createElement('div');
		menu.className = 'nx-xref-menu';
		menu.setAttribute('role', 'menu');
		const item = document.createElement('button');
		item.type = 'button';
		item.setAttribute('role', 'menuitem');
		item.dataset.action = 'xref';
		item.innerHTML = '<span>Find All References</span><kbd>Shift+F12</kbd>';
		item.title = `Every read and write of ${target.name} in the project, and its declaration`;
		item.addEventListener('click', (e) => {
			e.stopPropagation();
			closeMenu();
			post(target);
		});
		menu.appendChild(item);
		document.body.appendChild(menu);
		const w = menu.offsetWidth;
		const h = menu.offsetHeight;
		menu.style.left = `${Math.max(0, Math.min(ev.clientX, window.innerWidth - w - 2))}px`;
		menu.style.top = `${Math.max(0, Math.min(ev.clientY, window.innerHeight - h - 2))}px`;
		item.focus({ preventScroll: true });
	}, true);
}
