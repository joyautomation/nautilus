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
import { initialize, getService, ICommandService, IKeybindingService } from '@codingame/monaco-vscode-api';
import { StandardKeyboardEvent } from '@codingame/monaco-vscode-api/vscode/vs/base/browser/keyboardEvent';
import { ResultKind } from '@codingame/monaco-vscode-api/vscode/vs/platform/keybinding/common/keybindingResolver';
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
import getTerminal from '@codingame/monaco-vscode-terminal-service-override';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import TextMateWorker from '@codingame/monaco-vscode-textmate-service-override/worker?worker';
import extHostWorkerUrl from '@codingame/monaco-vscode-api/vscode/vs/workbench/api/worker/extensionHostWorkerMain?worker&url';

import { Playground } from '../src/client.js';
import { serveBridge } from '../src/bridge.js';
import { EngineFileSystem } from './engineFs.js';
import { NautShellBackend } from './nautShell.js';

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
	// For the page framing the playground (joyautomation.com's download
	// button): the project as it stands, saved edits included.
	window.nautilusPlayground = { title: project.title, files: () => pg.files() };
	registerFileSystemOverlay(1, new EngineFileSystem(pg));

	await initUserConfiguration(
		JSON.stringify({
			'workbench.colorTheme': 'Default Dark Modern',
			'workbench.startupEditor': 'none',
			'workbench.tips.enabled': false,
			'workbench.editor.enablePreview': false,
			'workbench.editorAssociations': { '*.scene.json': 'default', '*.kind.json': 'default' },
			'files.autoSave': 'afterDelay',
			'files.autoSaveDelay': 400,
			'editor.minimap.enabled': false,
			'security.workspace.trust.enabled': false,
			'telemetry.telemetryLevel': 'off',
			'terminal.integrated.defaultProfile.linux': 'nautilus',
			'terminal.integrated.defaultProfile.osx': 'nautilus',
			'terminal.integrated.defaultProfile.windows': 'nautilus',
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
			...getOutline(),
			// VS Code's terminal panel, with a nautilus shell for a backend
			// (nautShell.js): there is no machine to run bash on.
			...getTerminal(new NautShellBackend(pg))
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
	// Its files carry the build's ?v= like the engine's, so a returning
	// visitor gets the extension that matches this build, not a cached one.
	const v = params.get('v');
	const extUrl = (f) => new URL(f + (v ? '?v=' + encodeURIComponent(v) : ''), EXTENSION).href;
	const manifest = await (await fetch(extUrl('package.json'))).json();
	// The Marketplace extension is desktop-only for now (no "browser" entry,
	// so vscode.dev does not offer it without an engine); the playground
	// supplies the engine, so it names the web build itself.
	manifest.browser = './out/web/extension.js';
	const files = await (await fetch(extUrl('files.json'))).json();
	const ext = registerExtension(manifest, ExtensionHostKind.LocalWebWorker, { system: true });
	for (const f of files) ext.registerFileUrl('/' + f, extUrl(f));
	await ext.whenReady();
	splash?.remove();

	// For the framing page: keys typed while focus is outside this frame
	// (its nav, its toolbar) never reach the workbench, so it forwards the
	// command palette's keys here.
	const commands = await getService(ICommandService);
	relayWebviewKeys(commands, await getService(IKeybindingService));
	Object.assign(window.nautilusPlayground, {
		showCommands: () => commands.executeCommand('workbench.action.showCommands'),
		focus: () => commands.executeCommand('workbench.action.focusActiveEditorGroup')
	});
	window.dispatchEvent(new Event('nautilus-playground-ready'));
}

/**
 * Keys pressed inside a webview (the diagram editors) reach the workbench as
 * keydown events VS Code re-creates on this window (webviewElement.js,
 * handleKeyEvent). In this embedding the keybinding service does not act
 * on those synthetic events, so Ctrl+Shift+P, F1, Ctrl+S, Ctrl+` ... did
 * nothing while a diagram had focus. Resolve them the way the service
 * would (softDispatch, in the webview's context) and run the command. A
 * key the service did handle arrives here default-prevented and is left.
 */
function relayWebviewKeys(commands, keybindings) {
	window.addEventListener('keydown', (e) => {
		if (e.isTrusted || e.defaultPrevented) return;
		const target = e.target;
		if (!(target instanceof HTMLElement) || !target.classList.contains('webview')) return;
		const kb = new StandardKeyboardEvent(e);
		const res = keybindings.softDispatch(kb, target);
		if (res.kind !== ResultKind.KbFound || !res.commandId) return;
		e.preventDefault();
		void commands.executeCommand(res.commandId, ...(res.commandArgs === undefined ? [] : [res.commandArgs]));
	});
}

main().catch((e) => {
	console.error(e);
	fail('The playground could not start: ' + (e && e.message ? e.message : e));
});
