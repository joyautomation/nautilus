// Which diagram elements name a tag the live actions (Toggle Bit, Set Live
// Value…, Force…, Unforce) can act on, in every diagram: ladder contacts and
// coils, FBD variable chips, SFC action associations and transition
// conditions. The webview reports the selected element's tag to the host
// (diagramSelection), posts toggleBit for it on T, and gives the element a
// right-click context ({"nautilusTag": …}, package.json webview/context).
//
// The rule is the same everywhere: a plain identifier (not a member, an
// element or an expression; the controller's tag API writes whole tags) that
// is a variable of a PROGRAM. A FUNCTION_BLOCK's pins and locals are not
// tags; neither are literals, constants, steps or ACTIONs. Whether the
// controller has the tag, and whether it is a BOOL, is the host's call (it
// refuses with a message), as in the text editor.

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** The identifier, if `ref` is a plain one. */
export function plainTag(ref: string | undefined | null): string | undefined {
	const r = (ref ?? '').trim();
	return IDENT.test(r) ? r : undefined;
}

/** A ladder contact, edge contact or coil, outside a FUNCTION_BLOCK body
 * (whose rungs carry a scope). */
export function ldElementTag(rung: { scope?: unknown }, el: { kind: string; ref?: string }): string | undefined {
	if (rung.scope) return undefined;
	if (el.kind !== 'contact' && el.kind !== 'edge' && el.kind !== 'coil') return undefined;
	return plainTag(el.ref);
}

/** An FBD variable chip (an input read or a coil write), not a literal, in a
 * PROGRAM's body (`block`: the model is a FUNCTION_BLOCK's). */
export function fbdNodeTag(n: { kind: string; label: string; src?: unknown }, block: boolean | undefined): string | undefined {
	if (block) return undefined;
	if (n.kind !== 'input' && n.kind !== 'coil') return undefined;
	if (n.src !== undefined && n.src !== null) return undefined;
	return plainTag(n.label);
}

type SfcScope = {
	vars?: { name: string; section: string }[];
	tags?: { name: string }[];
	steps?: { name: string }[];
	actions?: { name: string }[];
};

/** An SFC name that is a variable of the chart: declared (not a constant) or
 * a manifest tag, and not a step or an ACTION. An association target that is
 * none of these is a new action waiting for its body, not a tag. */
export function sfcNameTag(name: string | undefined, m: SfcScope): string | undefined {
	const t = plainTag(name);
	if (!t) return undefined;
	const k = t.toLowerCase();
	const is = (xs: { name: string }[] | undefined) => (xs ?? []).some((x) => x.name.toLowerCase() === k);
	if (is(m.actions) || is(m.steps)) return undefined;
	const v = (m.vars ?? []).find((x) => x.name.toLowerCase() === k);
	if (v) return /CONSTANT/i.test(v.section) ? undefined : t;
	return is(m.tags) ? t : undefined;
}

/** The right-click context for an element's tag, merged into whatever
 * context the element already has (VS Code merges it with its ancestors'). */
export function tagContext(tag: string | undefined, base: Record<string, unknown> = {}): string | undefined {
	if (!tag && !Object.keys(base).length) return undefined;
	return JSON.stringify(tag ? { ...base, nautilusTag: tag } : base);
}

/** T (no Ctrl/Cmd/Alt): Toggle Bit on the selection. Ctrl/Cmd+T is the
 * workbench keybinding's, so the webview never also toggles on it. */
export function isToggleKey(ev: { key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean }): boolean {
	return !ev.ctrlKey && !ev.metaKey && !ev.altKey && (ev.key === 't' || ev.key === 'T');
}
