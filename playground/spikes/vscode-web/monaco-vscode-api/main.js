// A minimal VS Code workbench from monaco-vscode-api parts, running the same
// nautilus stub extension as the VS Code web measurement.
import '@codingame/monaco-vscode-theme-defaults-default-extension';
import { initialize } from '@codingame/monaco-vscode-api';
import { ExtensionHostKind, registerExtension } from '@codingame/monaco-vscode-api/extensions';
import getWorkbench from '@codingame/monaco-vscode-workbench-service-override';
import getExtensions from '@codingame/monaco-vscode-extensions-service-override';
import getFiles from '@codingame/monaco-vscode-files-service-override';
import getTheme from '@codingame/monaco-vscode-theme-service-override';
import getKeybindings from '@codingame/monaco-vscode-keybindings-service-override';
import getQuickAccess from '@codingame/monaco-vscode-quickaccess-service-override';
import getConfiguration from '@codingame/monaco-vscode-configuration-service-override';
import getExplorer from '@codingame/monaco-vscode-explorer-service-override';
import getTextmate from '@codingame/monaco-vscode-textmate-service-override';
import getModel from '@codingame/monaco-vscode-model-service-override';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import TextMateWorker from '@codingame/monaco-vscode-textmate-service-override/worker?worker';
import extHostWorkerUrl from '@codingame/monaco-vscode-api/vscode/vs/workbench/api/worker/extensionHostWorkerMain?worker&url';

self.MonacoEnvironment = {
	getWorkerUrl: (_id, label) => (label === 'extensionHostWorkerMain' ? new URL(extHostWorkerUrl, location.href).href : undefined),
	getWorkerOptions: (_id, label) => (label === 'extensionHostWorkerMain' ? { type: 'module' } : undefined),
	getWorker: (_id, label) =>
		label === 'TextMateWorker' ? new TextMateWorker() : label === 'extensionHostWorkerMain' ? new Worker(extHostWorkerUrl, { type: 'module' }) : new EditorWorker()
};

await initialize(
	{
		...getWorkbench(),
		...getExtensions({ enableWorkerExtensionHost: true }),
		...getFiles(),
		...getTheme(),
		...getKeybindings(),
		...getQuickAccess(),
		...getConfiguration(),
		...getExplorer(),
		...getTextmate(),
		...getModel()
	},
	document.getElementById('workbench'),
	{ workspaceProvider: { trusted: true, workspace: undefined, async open() { return false; } } }
);

const manifest = await (await fetch('/ext/package.json')).json();
manifest.spikeBase = location.origin + '/ext/';
const ext = registerExtension(manifest, ExtensionHostKind.LocalWebWorker);
ext.registerFileUrl('/package.json', new URL('/ext/package.json', location.href).href);
ext.registerFileUrl('/extension.js', new URL('/ext/extension.js', location.href).href);
await ext.whenReady();
