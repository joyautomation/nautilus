// The VS Code theme KIND, read from the classes VS Code puts on <body>
// (vscode-light / vscode-dark / vscode-high-contrast[-light]) and kept
// current across theme switches. Colours themselves come from the
// --vscode-* variables (theme.css); this is only for libraries that need a
// light/dark switch of their own — xyflow's colorMode, whose MiniMap and
// Controls defaults are otherwise light-on-dark washed out.

export type ThemeKind = 'light' | 'dark' | 'hc-dark' | 'hc-light';

function read(): ThemeKind {
	const c = document.body?.classList;
	if (!c) return 'dark';
	if (c.contains('vscode-high-contrast-light')) return 'hc-light';
	if (c.contains('vscode-high-contrast')) return 'hc-dark';
	if (c.contains('vscode-light')) return 'light';
	return 'dark';
}

export const themeColorMode = $state({
	kind: read() as ThemeKind,
	get mode(): 'light' | 'dark' {
		return this.kind === 'light' || this.kind === 'hc-light' ? 'light' : 'dark';
	}
});

if (typeof MutationObserver !== 'undefined' && document.body) {
	new MutationObserver(() => {
		const k = read();
		if (k !== themeColorMode.kind) themeColorMode.kind = k;
	}).observe(document.body, { attributes: true, attributeFilter: ['class'] });
}
