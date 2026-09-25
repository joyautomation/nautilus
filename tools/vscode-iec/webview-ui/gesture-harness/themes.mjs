// Stand-ins for what VS Code injects into a webview per theme: the body
// class and the --vscode-* variables the diagram's theme.css consumes (the
// values of the built-in Dark Modern / Light Modern / High Contrast
// themes). Applying one reproduces a theme in the harness, so tests and
// screenshots can check every surface against light, dark and HC.

export const THEMES = {
	dark: {
		cls: 'vscode-dark',
		vars: {
			'editor-background': '#1f1f1f',
			'editorWidget-background': '#202020',
			'input-background': '#313131',
			'editor-foreground': '#cccccc',
			foreground: '#cccccc',
			'input-foreground': '#cccccc',
			descriptionForeground: '#9d9d9d',
			'editorLineNumber-foreground': '#6e7681',
			'editorWidget-border': '#313131',
			focusBorder: '#0078d4',
			'charts-blue': '#3794ff',
			'textLink-foreground': '#4daafc',
			'charts-green': '#89d185',
			'editorWarning-foreground': '#cca700',
			errorForeground: '#f85149',
			'gitDecoration-addedResourceForeground': '#81b88b',
			'gitDecoration-deletedResourceForeground': '#c74e39',
			'gitDecoration-modifiedResourceForeground': '#e2c08d',
			'terminal-ansiCyan': '#11a8cd',
			'list-hoverBackground': '#2a2d2e',
			'list-activeSelectionBackground': '#04395e',
			'list-activeSelectionForeground': '#ffffff',
			'button-background': '#0078d4',
			'button-foreground': '#ffffff'
		}
	},
	light: {
		cls: 'vscode-light',
		vars: {
			'editor-background': '#ffffff',
			'editorWidget-background': '#f8f8f8',
			'input-background': '#ffffff',
			'editor-foreground': '#3b3b3b',
			foreground: '#3b3b3b',
			'input-foreground': '#3b3b3b',
			descriptionForeground: '#3b3b3b',
			'editorLineNumber-foreground': '#6e7681',
			'editorWidget-border': '#e5e5e5',
			focusBorder: '#005fb8',
			'charts-blue': '#1a85ff',
			'textLink-foreground': '#005fb8',
			'charts-green': '#388a34',
			'editorWarning-foreground': '#bf8803',
			errorForeground: '#f85149',
			'gitDecoration-addedResourceForeground': '#587c0c',
			'gitDecoration-deletedResourceForeground': '#ad0707',
			'gitDecoration-modifiedResourceForeground': '#895503',
			'terminal-ansiCyan': '#0598bc',
			'list-hoverBackground': '#f2f2f2',
			'list-activeSelectionBackground': '#e8e8e8',
			'list-activeSelectionForeground': '#000000',
			'button-background': '#005fb8',
			'button-foreground': '#ffffff'
		}
	},
	hc: {
		cls: 'vscode-high-contrast',
		vars: {
			'editor-background': '#000000',
			'editorWidget-background': '#0c141f',
			'input-background': '#000000',
			'editor-foreground': '#ffffff',
			foreground: '#ffffff',
			'input-foreground': '#ffffff',
			descriptionForeground: '#ffffff',
			'editorLineNumber-foreground': '#ffffff',
			'editorWidget-border': '#6fc3df',
			focusBorder: '#f38518',
			contrastActiveBorder: '#f38518',
			contrastBorder: '#6fc3df',
			'charts-blue': '#3794ff',
			'textLink-foreground': '#21a6ff',
			'charts-green': '#89d185',
			'editorWarning-foreground': '#ffd370',
			errorForeground: '#f48771',
			'gitDecoration-addedResourceForeground': '#a1e3ad',
			'gitDecoration-deletedResourceForeground': '#c74e39',
			'gitDecoration-modifiedResourceForeground': '#e2c08d',
			'terminal-ansiCyan': '#00cdcd',
			'list-activeSelectionForeground': '#ffffff',
			'button-background': '#000000',
			'button-foreground': '#ffffff'
		}
	}
};

/** Page-side JS that applies a theme (body class + variables on <html>,
 * where VS Code puts them). */
export function applyThemeJs(name) {
	const t = THEMES[name];
	return `(() => {
		document.body.classList.remove('vscode-dark', 'vscode-light', 'vscode-high-contrast', 'vscode-high-contrast-light');
		document.body.classList.add(${JSON.stringify(t.cls)});
		const vars = ${JSON.stringify(t.vars)};
		const st = document.documentElement.style;
		for (let i = st.length - 1; i >= 0; i--) if (st[i].startsWith('--vscode-')) st.removeProperty(st[i]);
		for (const [k, v] of Object.entries(vars)) st.setProperty('--vscode-' + k, v);
		return true;
	})()`;
}
