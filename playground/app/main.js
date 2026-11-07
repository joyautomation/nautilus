// The nautilus playground: VS Code's workbench (monaco-vscode-api) running
// the nautilus extension's web build, with nautilus itself (naut.wasm) in a
// worker on this page. Nothing is installed and nothing leaves the browser.
//
//   ?example=<name>   one of ../examples/index.json (default: sealin)
//   ?project=<url>    a project JSON: {path: text} or {files, open, title}
import '@codingame/monaco-vscode-theme-defaults-default-extension';
import '@codingame/monaco-vscode-yaml-default-extension';
import '@codingame/monaco-vscode-json-default-extension';
import '@codingame/monaco-vscode-markdown-basics-default-extension';
import { initialize } from '@codingame/monaco-vscode-api';
import { ExtensionHostKind, registerExtension } from '@codingame/monaco-vscode-api/extensions';
import { URI } from '@codingame/monaco-vscode-api/vscode/vs/base/common/uri';
import getWorkbench from '@codingame/monaco-vscode-workbench-service-override';
import getExtensions from '@codingame/monaco-vscode-extensions-service-override';
import getFiles, { registerFileSystemOverlay } from '@codingame/monaco-vscode-files-service-override';
import getTheme from '@codingame/monaco-vscode-theme-service-override';
import getKeybindings from '@codingame/monaco-vscode-keybindings-service-override';
import getQuickAccess from '@codingame/monaco-vscode-quickaccess-service-override';
import getConfiguration, { initUserConfiguration } from '@codingame/monaco-vscode-configuration-service-override';
import getExplorer from '@codingame/monaco-vscode-explorer-service-override';
import getTextmate from '@codingame/monaco-vscode-textmate-service-override';
import getModel from '@codingame/monaco-vscode-model-service-override';
import getLanguages from '@codingame/monaco-vscode-languages-service-override';
import getMarkers from '@codingame/monaco-vscode-markers-service-override';
import getOutput from '@codingame/monaco-vscode-output-service-override';
import getNotifications from '@codingame/monaco-vscode-notifications-service-override';
import getTesting from '@codingame/monaco-vscode-testing-service-override';
import getLog from '@codingame/monaco-vscode-log-service-override';
import getOutline from '@codingame/monaco-vscode-outline-service-override';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import TextMateWorker from '@codingame/monaco-vscode-textmate-service-override/worker?worker';
import extHostWorkerUrl from '@codingame/monaco-vscode-api/vscode/vs/workbench/api/worker/extensionHostWorkerMain?worker&url';

import { Playground } from '../src/client.js';
import { serveBridge } from '../src/bridge.js';
import { EngineFileSystem } from './engineFs.js';

const ENGINE = new URL('../', location.href); // the package's dist/: worker.js, naut.wasm, examples/
const EXTENSION = new URL('./extension/', location.href); // the extension's web build and assets
const PLAYGROUND_URL = 'http://playground.nautilus'; // the extension's controller URL (src/web/engine.ts)

self.MonacoEnvironment = {
	getWorkerUrl: (_id, label) => (label === 'extensionHostWorkerMain' ? new URL(extHostWorkerUrl, location.href).href : undefined),
	getWorkerOptions: (_id, label) => (label === 'extensionHostWorkerMain' ? { type: 'module' } : undefined),
	getWorker: (_id, label) =>
		label === 'TextMateWorker'
			? new TextMateWorker()
			: label === 'extensionHostWorkerMain'
				? new Worker(extHostWorkerUrl, { type: 'module' })
				: new EditorWorker()
};

const splash = document.getElementById('splash');
const say = (t) => splash && (splash.querySelector('p').textContent = t);
const fail = (t) => {
	if (!splash) return;
	splash.classList.add('failed');
	splash.querySelector('p').textContent = t;
};

async function loadProject(params) {
	const url = params.get('project');
	if (url) {
		const r = await fetch(url);
		if (!r.ok) throw new Error(`could not fetch ${url} (${r.status})`);
		const data = await r.json();
		const files = data && typeof data.files === 'object' ? data.files : data;
		if (!files || !('nautilus.yaml' in files)) throw new Error(`${url} is not a nautilus project (no nautilus.yaml)`);
		return { title: data.title || 'project', open: data.open || ['nautilus.yaml'], files };
	}
	const name = params.get('example') || 'sealin';
	const r = await fetch(new URL(`examples/${encodeURIComponent(name)}.json`, ENGINE));
	if (!r.ok) throw new Error(`no example named "${name}"`);
	return r.json();
}

/** The project's files to open, first one active. (The extension opens a
 * diagram file's diagram beside its text once its editors are registered:
 * src/web/platform.ts.) */
function defaultEditors(open) {
	return open.map((f, i) => ({
		uri: URI.file('/project/' + f),
		viewColumn: 1,
		openOnlyIfExists: true,
		options: i === 0 ? { pinned: true } : { inactive: true, pinned: true }
	}));
}

async function main() {
	const params = new URLSearchParams(location.search);
	say('Starting nautilus in your browser…');
	const [pg, project] = await Promise.all([
		Playground.start({ base: ENGINE.href, version: params.get('v') || undefined }),
		loadProject(params)
	]);
	await pg.setProject(project.files);
	document.title = `${project.title} · nautilus playground`;

	const bridge = serveBridge(pg);
	registerFileSystemOverlay(1, new EngineFileSystem(pg));

	await initUserConfiguration(
		JSON.stringify({
			'workbench.colorTheme': 'Default Dark Modern',
			'workbench.startupEditor': 'none',
			'workbench.tips.enabled': false,
			'workbench.editor.enablePreview': false,
			'workbench.editorAssociations': { '*.mimic.json': 'default', '*.scene.json': 'default', '*.kind.json': 'default' },
			'files.autoSave': 'afterDelay',
			'files.autoSaveDelay': 400,
			'editor.minimap.enabled': false,
			'security.workspace.trust.enabled': false,
			'telemetry.telemetryLevel': 'off',
			'nautilus.playground.channel': bridge.name,
			'nautilus.runtimeUrl': PLAYGROUND_URL
		})
	);

	say('Opening the editor…');
	await initialize(
		{
			...getLog(),
			...getWorkbench(),
			...getExtensions({ enableWorkerExtensionHost: true }),
			...getFiles(),
			...getTheme(),
			...getKeybindings(),
			...getQuickAccess(),
			...getConfiguration(),
			...getExplorer(),
			...getTextmate(),
			...getModel(),
			...getLanguages(),
			...getMarkers(),
			...getOutput(),
			...getNotifications(),
			...getTesting(),
			...getOutline()
		},
		document.getElementById('workbench'),
		{
			workspaceProvider: { trusted: true, workspace: { folderUri: URI.file('/project') }, async open() { return false; } },
			defaultLayout: (() => {
				const editors = defaultEditors(project.open || []);
				// A diagram beside the text needs the second editor group to
				// exist: the workbench places editors by viewColumn.
				const columns = Math.max(1, ...editors.map((e) => e.viewColumn ?? 1));
				return {
					force: true,
					editors,
					layout: { editors: { orientation: 0, groups: Array.from({ length: columns }, () => ({ size: 1 / columns })) } }
				};
			})(),
			productConfiguration: { nameShort: 'nautilus playground', nameLong: 'nautilus playground' }
		}
	);

	// The nautilus extension, as built for the web (tools/vscode-iec).
	const manifest = await (await fetch(new URL('package.json', EXTENSION))).json();
	// The Marketplace extension is desktop-only for now (no "browser" entry,
	// so vscode.dev does not offer it without an engine); the playground
	// supplies the engine, so it names the web build itself.
	manifest.browser = './out/web/extension.js';
	const files = await (await fetch(new URL('files.json', EXTENSION))).json();
	const ext = registerExtension(manifest, ExtensionHostKind.LocalWebWorker, { system: true });
	for (const f of files) ext.registerFileUrl('/' + f, new URL(f, EXTENSION).href);
	await ext.whenReady();
	splash?.remove();
}

main().catch((e) => {
	console.error(e);
	fail('The playground could not start: ' + (e && e.message ? e.message : e));
});
