// The nautilus playground: VS Code's workbench (monaco-vscode-api) running
// the nautilus extension's web build, with nautilus itself (naut.wasm) in a
// worker on this page. Nothing is installed, and nothing leaves the browser
// unless the visitor shares the project (share/).
//
//   ?example=<name>   one of ../examples/index.json (default: sealin)
//   ?project=<url>    a project JSON: {path: text} or {files, open, title}
//   ?p=<id>           a saved project (/playground/p/<id> on the docs site;
//                     playground/share keeps them)
//   #project=<code>   a share link: the project itself (keep.js)
//
// The visitor's edits are kept in their browser, apart from the project
// they started from (keep.js): a reload or a later visit brings them back.
import '@codingame/monaco-vscode-theme-defaults-default-extension';
import '@codingame/monaco-vscode-yaml-default-extension';
import '@codingame/monaco-vscode-json-default-extension';
import '@codingame/monaco-vscode-markdown-basics-default-extension';
import {
	initialize,
	getService,
	ICommandService,
	IEditorService,
	IKeybindingService,
	INotificationService,
	IDialogService,
	IClipboardService
} from '@codingame/monaco-vscode-api';
import { Action2, registerAction2 } from '@codingame/monaco-vscode-api/vscode/vs/platform/actions/common/actions';
import Severity from '@codingame/monaco-vscode-api/vscode/vs/base/common/severity';
import { StandardKeyboardEvent } from '@codingame/monaco-vscode-api/vscode/vs/base/browser/keyboardEvent';
import { ResultKind } from '@codingame/monaco-vscode-api/vscode/vs/platform/keybinding/common/keybindingResolver';
import { ExtensionHostKind, registerExtension } from '@codingame/monaco-vscode-api/extensions';
import { URI } from '@codingame/monaco-vscode-api/vscode/vs/base/common/uri';
import getWorkbench from '@codingame/monaco-vscode-workbench-service-override';
import getExtensions from '@codingame/monaco-vscode-extensions-service-override';
import getFiles, { registerFileSystemOverlay } from '@codingame/monaco-vscode-files-service-override';
import getWorkingCopy from '@codingame/monaco-vscode-working-copy-service-override';
import getDialogs from '@codingame/monaco-vscode-dialogs-service-override';
import getSearch from '@codingame/monaco-vscode-search-service-override';
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
import * as kept from './keep.js';
import { projectFileSearch } from './fileSearch.js';
import { ISearchService } from '@codingame/monaco-vscode-api/vscode/vs/workbench/services/search/common/search.service';
import { SearchProviderType } from '@codingame/monaco-vscode-api/vscode/vs/workbench/services/search/common/search';
import { Schemas } from '@codingame/monaco-vscode-api/vscode/vs/base/common/network';

const ENGINE = new URL('../', location.href); // the package's dist/: worker.js, naut.wasm, examples/
const EXTENSION = new URL('./extension/', location.href); // the extension's web build and assets
const PLAYGROUND_URL = 'http://playground.nautilus'; // the extension's controller URL (src/web/engine.ts)
const SAVED = new URL('/playground/api/p', location.origin); // playground/share, beside the docs site

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

/**
 * The project this page was opened on, as published: {key, kind, project}.
 * key names it in the visitor's kept copies; kind is example, url, saved
 * or share. drifted names the example a share link was made from when
 * that example has changed since.
 */
async function loadSource(params) {
	const code = new URLSearchParams(location.hash.slice(1)).get('project');
	if (code) {
		const shared = await kept.decodeProject(code);
		const key = 'share:' + (await kept.fingerprint({ code }));
		if (!shared.from) return { key, kind: 'share', project: { ...shared, open: shared.open ?? ['nautilus.yaml'] } };
		// Changes to one of our examples: laid over the example as it is now.
		const example = await fetchExample(shared.from);
		const files = { ...example.files, ...shared.files };
		for (const f of shared.removed) delete files[f];
		const drifted = shared.base && shared.base !== (await kept.fingerprint(example.files)) ? example.title : undefined;
		return { key, kind: 'share', drifted, project: { ...example, title: shared.title, open: shared.open ?? example.open, files } };
	}
	const id = params.get('p');
	if (id) {
		if (!/^[a-z2-7]{12}$/.test(id)) throw new Error('this link does not name a saved project');
		const r = await fetch(`${SAVED}/${id}`);
		if (r.status === 410) throw new Error('the project at this link was taken down');
		if (!r.ok) throw new Error(r.status === 404 ? 'there is no saved project at this link' : `could not load the saved project (${r.status})`);
		const p = await r.json();
		return { key: 'saved:' + id, kind: 'saved', project: { ...p, open: p.open?.length ? p.open : ['nautilus.yaml'] } };
	}
	const url = params.get('project');
	if (url) {
		const r = await fetch(url);
		if (!r.ok) throw new Error(`could not fetch ${url} (${r.status})`);
		const data = await r.json();
		const files = data && typeof data.files === 'object' ? data.files : data;
		if (!files || !('nautilus.yaml' in files)) throw new Error(`${url} is not a nautilus project (no nautilus.yaml)`);
		return { key: 'url:' + url, kind: 'url', project: { title: data.title || 'project', open: data.open || ['nautilus.yaml'], files } };
	}
	const name = params.get('example') || 'sealin';
	return { key: 'example:' + name, kind: 'example', project: await fetchExample(name) };
}

async function fetchExample(name) {
	// Never ?v=: an example edited on the site reaches visitors on their
	// next load (nginx has the browser revalidate it).
	const r = await fetch(new URL(`examples/${encodeURIComponent(name)}.json`, ENGINE));
	if (!r.ok) throw new Error(`no example named "${name}"`);
	return r.json();
}

/**
 * Where a share link points: the page framing the playground when there
 * is one (the docs site's /playground/), else this page.
 */
function shareBase() {
	try {
		if (window.parent !== window) {
			const at = window.parent.location;
			return at.origin + at.pathname.replace(/\/p\/[a-z2-7]+\/?$/, '/');
		}
	} catch {
		// Framed by another site: link to the playground itself.
	}
	const u = new URL(location.href);
	u.hash = '';
	u.searchParams.delete('v');
	return u.href;
}

/**
 * Keeps the visitor's edits: after each change made in the workbench, the
 * project as it stands is saved under the source's key, or forgotten when
 * it matches the source again. Returns the hooks main() needs.
 */
function keeper(pg, source, copy) {
	const { key, project } = source;
	let base = copy ? copy.base : project.version; // the source version their copy started from
	let timer = 0;
	let stopped = false;
	const save = async () => {
		if (stopped) return;
		const files = await pg.files();
		if (stopped) return;
		if (kept.sameFiles(files, project.files)) await kept.forget(key);
		else await kept.keep({ key, base, files, updated: Date.now() }).catch((e) => console.warn('nautilus playground: could not keep your changes:', e));
	};
	return {
		edited() {
			clearTimeout(timer);
			timer = setTimeout(save, 300);
		},
		/** Their copy stays, now counted as made from the current source. */
		async adopt() {
			base = project.version;
			await save();
		},
		/** Back to the source: the copy is dropped and the page reloads. */
		async discard() {
			stopped = true;
			clearTimeout(timer);
			await kept.forget(key);
			location.reload();
		}
	};
}

/**
 * The pages `naut run` would serve, opened in tabs of their own: the
 * built-in dashboard and, for an example that ships one built, the
 * project's HMI. sw.js serves them from this tab's engine (its channel is
 * in the URL). Resolves to their URLs, or null where service workers are
 * unavailable. The worker's scope is view/ alone, so the workbench's own
 * requests never pass through it.
 */
async function serveViews(channel, hmi) {
	try {
		if (!('serviceWorker' in navigator)) return null;
		const reg = await navigator.serviceWorker.register(new URL('sw.js', ENGINE), {
			scope: new URL('view/', ENGINE).pathname,
			updateViaCache: 'none'
		});
		const sw = reg.installing || reg.waiting || reg.active;
		if (sw && sw.state !== 'activated') {
			await new Promise((ok, no) =>
				sw.addEventListener('statechange', () =>
					sw.state === 'activated' ? ok() : sw.state === 'redundant' && no(new Error('sw.js did not install'))
				)
			);
		}
		// The newest playground tab is where a view goes when its own tab is
		// reloaded or closed. (sw, not reg.active: during an update that is
		// still the old worker.)
		(sw ?? reg.active)?.postMessage({ kind: 'engine', channel });
		const url = (name) => new URL(`view/${name}/?engine=${encodeURIComponent(channel)}`, ENGINE).href;
		return { dashboard: url('dashboard'), hmi: hmi ? url(hmi) : null };
	} catch (e) {
		console.warn('nautilus playground: no dashboard or HMI view:', e);
		return null;
	}
}

/**
 * The project's files to open, first one showing. Opened once the extension
 * is registered, not in the workbench's default layout: the layout opens its
 * editors before the extension's diagram editors exist, so a ladder file
 * would come up as text. Here they resolve through editorAssociations.
 */
async function openProject(project) {
	const editors = await getService(IEditorService);
	const open = (project.open || []).filter((f) => f in project.files);
	if (!open.length) return;
	await editors.openEditor({ resource: URI.file('/project/' + open[0]), options: { pinned: true } });
	// Each lands just right of the showing one: the rest, last first, keeps
	// their order. Inactive, so a diagram builds its view only when shown.
	for (const f of open.slice(1).reverse()) {
		await editors.openEditor({ resource: URI.file('/project/' + f), options: { pinned: true, inactive: true, preserveFocus: true } });
	}
}

async function main() {
	// VS Code's extension host and webviews need crypto.subtle, which
	// browsers give only to https pages and localhost: on a plain-http
	// address (a LAN or tailnet preview) the workbench would never open.
	if (!window.isSecureContext) {
		fail('The playground needs a secure page: open it over https, or on localhost. Browsers give VS Code what it needs only there.');
		return;
	}
	const params = new URLSearchParams(location.search);
	say('Starting nautilus in your browser…');
	const [pg, source] = await Promise.all([
		Playground.start({ base: ENGINE.href, version: params.get('v') || undefined }),
		loadSource(params)
	]);
	source.project.version = await kept.fingerprint(source.project.files);
	const copy = await kept.mine(source.key);
	// What opens: their copy if they have one, else the source as published.
	const project = copy ? { ...source.project, files: copy.files } : source.project;
	const keep = keeper(pg, source, copy);
	await pg.setProject(project.files);
	document.title = `${project.title} · nautilus playground`;

	const bridge = serveBridge(pg);
	const views = serveViews(bridge.name, project.hmi);
	// For the page framing the playground (the docs site's download
	// button): the project as it stands, saved edits included.
	// A project that began as one of our examples (or a link to one) is
	// shared as its changes to that example.
	const example = source.kind === 'example' ? source.project : project.name ? await fetchExample(project.name).catch(() => null) : null;
	const shareLink = async () => {
		const files = await pg.files();
		// An example as published: a link to the example itself.
		if (source.kind === 'example' && kept.sameFiles(files, source.project.files)) {
			return shareBase() + '?example=' + encodeURIComponent(params.get('example') || 'sealin');
		}
		// Saved by playground/share: a short link that always opens this
		// version. Without it (the service is down, or this is a dev
		// server), the whole project goes in the link instead.
		try {
			const { title, open, hmi, name } = project;
			const r = await fetch(SAVED, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ title, open, hmi, name, files })
			});
			const out = await r.json().catch(() => ({}));
			if (r.ok && out.id) return new URL(`/playground/p/${out.id}`, location.origin).href;
			if (r.status === 400 || r.status === 413) throw Object.assign(new Error(out.error || 'the project could not be saved'), { refused: true });
			console.warn('nautilus playground: not saved, sharing it in the link:', r.status, out.error);
		} catch (e) {
			if (e.refused) throw e;
			console.warn('nautilus playground: not saved, sharing it in the link:', e);
		}
		const base = example && (await kept.fingerprint(example.files));
		return shareBase() + '#project=' + (await kept.encodeProject(kept.sharePayload(project, files, example, base)));
	};
	window.nautilusPlayground = { title: project.title, files: () => pg.files(), shareLink };
	registerPlaygroundCommands(source, keep, shareLink);
	registerFileSystemOverlay(1, new EngineFileSystem(pg, '/project', keep.edited));

	await initUserConfiguration(
		JSON.stringify({
			'workbench.colorTheme': 'Default Dark Modern',
			'workbench.startupEditor': 'none',
			'workbench.tips.enabled': false,
			'workbench.editor.enablePreview': false,
			// The graphical languages open in their diagram editors, as a
			// mimic opens in its own; Reopen Editor With shows the text.
			'workbench.editorAssociations': {
				'*.ld': 'nautilus.ldDiagram',
				'*.fbd': 'nautilus.fbdDiagram',
				'*.sfc': 'nautilus.sfcDiagram',
				'*.scene.json': 'default',
				'*.kind.json': 'default'
			},
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
			// Without it, edits never reach the save machinery: files.autoSave
			// does nothing and a typed change is lost unless saved by hand.
			// Backups in memory only: what lasts across visits is keep.js's.
			...getWorkingCopy({ storage: 'memory' }),
			// VS Code's own dialogs, not the browser's confirm() (Discard My Changes,
			// the explorer's delete).
			...getDialogs(),
			// File search over the project (the engine's file system): without
			// it workspace.findFiles answers with the open editor whatever the
			// glob, and the extension's component index, test discovery and
			// custom components find the wrong files. Also the Search view.
			...getSearch(),
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
			// One editor group, empty until openProject: no editors restored
			// from a previous visit.
			defaultLayout: { force: true, editors: [], layout: { editors: { orientation: 0, groups: [{ size: 1 }] } } },
			productConfiguration: { nameShort: 'nautilus playground', nameLong: 'nautilus playground' }
		}
	);

	// findFiles and Quick Open over the project, globs honoured (fileSearch.js).
	const search = await getService(ISearchService);
	search.registerSearchResultProvider(
		Schemas.file,
		SearchProviderType.file,
		projectFileSearch(async () => Object.keys(await pg.files()))
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
	await openProject(project);
	splash?.remove();
	if (copy) void welcomeBack(source, copy, keep);
	if (source.drifted) void driftNotice(source.drifted);

	// For the framing page: keys typed while focus is outside this frame
	// (its nav, its toolbar) never reach the workbench, so it forwards the
	// command palette's keys here.
	const commands = await getService(ICommandService);
	relayWebviewKeys(commands, await getService(IKeybindingService));
	const notifications = await getService(INotificationService);
	Object.assign(window.nautilusPlayground, {
		/** The framing page's Share button copied a link. */
		shared: () => notifications.info(SHARED),
		showCommands: () => commands.executeCommand('workbench.action.showCommands'),
		focus: () => commands.executeCommand('workbench.action.focusActiveEditorGroup'),
		views: await views
	});
	window.dispatchEvent(new Event('nautilus-playground-ready'));
}

/** Said whenever a share link is copied: a saved project is public to its link. */
const SHARED = 'Link copied. Anyone with the link can open this project and get their own copy to change.';

const SOURCE_NAME = { example: 'example', url: 'project', saved: 'saved project', share: 'shared project' };

/** A share link's changes were laid over an example that has changed since. */
async function driftNotice(example) {
	const notifications = await getService(INotificationService);
	notifications.prompt(
		Severity.Warning,
		`This link holds changes to the ${example} example, which has been updated since the link was made. Some of the changes may not fit the new version.`,
		[],
		{ sticky: true }
	);
}

/** Their copy is back: say so, and whether the source changed since. */
async function welcomeBack(source, copy, keep) {
	const notifications = await getService(INotificationService);
	const what = `the ${source.project.title} ${SOURCE_NAME[source.kind]}`;
	if (copy.base !== source.project.version) {
		notifications.prompt(
			Severity.Info,
			`${cap(what)} has been updated since you started changing it. You are looking at your version.`,
			[
				{ label: 'Open the new version', run: () => keep.discard() },
				{ label: 'Keep mine', run: () => keep.adopt() }
			],
			{ sticky: true }
		);
	} else {
		// Sticky: an ordinary toast is gone before the editor has settled.
		const commands = await getService(ICommandService);
		notifications.prompt(
			Severity.Info,
			`Your changes to ${what} are back. They are kept in this browser.`,
			[{ label: 'Start over', run: () => commands.executeCommand('nautilus.playground.discard') }],
			{ sticky: true }
		);
	}
}

const cap = (s) => s[0].toUpperCase() + s.slice(1);

/** The playground's own commands, in the command palette. */
function registerPlaygroundCommands(source, keep, shareLink) {
	const category = { value: 'Playground', original: 'Playground' };
	registerAction2(
		class extends Action2 {
			constructor() {
				super({ id: 'nautilus.playground.discard', title: { value: 'Discard My Changes', original: 'Discard My Changes' }, category, f1: true });
			}
			async run(accessor) {
				const dialogs = accessor.get(IDialogService);
				const { confirmed } = await dialogs.confirm({
					message: `Discard your changes to the ${source.project.title} ${SOURCE_NAME[source.kind]}?`,
					detail: 'The original comes back. To keep a copy of your version, use Download project first.',
					primaryButton: 'Discard my changes'
				});
				if (confirmed) await keep.discard();
			}
		}
	);
	registerAction2(
		class extends Action2 {
			constructor() {
				super({ id: 'nautilus.playground.share', title: { value: 'Copy Share Link', original: 'Copy Share Link' }, category, f1: true });
			}
			async run(accessor) {
				const clipboard = accessor.get(IClipboardService);
				const notifications = accessor.get(INotificationService);
				try {
					await clipboard.writeText(await shareLink());
					notifications.info(SHARED);
				} catch (e) {
					notifications.error(`Could not share the project: ${e.message}`);
				}
			}
		}
	);
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
