// Svelte syntax colouring for a project's .svelte components. The grammar
// alone: the Svelte extension's language server needs Node, so there are
// no completions or diagnostics here (the component preview shows compile
// errors). The grammar is the Svelte extension's own
// (sveltejs/language-tools, MIT), as the tm-grammars package ships it.
// Its <script> and <style> blocks are coloured by the TypeScript,
// JavaScript and CSS grammars, so main.js loads those too.
import { registerExtension } from '@codingame/monaco-vscode-api/extensions';
import grammarUrl from 'tm-grammars/grammars/svelte.json?url';
import configurationUrl from './svelte/language-configuration.json?url';

const manifest = {
	name: 'svelte-syntax',
	publisher: 'nautilus-playground',
	version: '1.0.0',
	engines: { vscode: '*' },
	contributes: {
		languages: [
			{
				id: 'svelte',
				aliases: ['Svelte', 'svelte'],
				extensions: ['.svelte'],
				configuration: './language-configuration.json'
			}
		],
		grammars: [
			{
				language: 'svelte',
				scopeName: 'source.svelte',
				path: './syntaxes/svelte.tmLanguage.json',
				// The embedded languages this app has grammars for.
				embeddedLanguages: {
					'source.css': 'css',
					'source.js': 'javascript',
					'source.ts': 'typescript',
					'text.html.markdown': 'markdown'
				}
			}
		]
	}
};

const { registerFileUrl, whenReady } = registerExtension(manifest, undefined, { system: true });
const url = (u) => new URL(u, import.meta.url).href;
registerFileUrl('language-configuration.json', url(configurationUrl), { mimeType: 'application/json' });
registerFileUrl('syntaxes/svelte.tmLanguage.json', url(grammarUrl), { mimeType: 'application/json' });

export { whenReady };
