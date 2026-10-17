import { mount } from 'svelte';
import '../theme.css';
import './mimic.css';
import MimicApp from './MimicApp.svelte';
import ComponentApp from './ComponentApp.svelte';
import PreviewApp from './PreviewApp.svelte';

// One bundle, three panes: the extension host stamps which one opened it
// onto <body data-mimic-mode> — "mimic" (mimicEditor.ts) mounts the full
// P&ID editor, "component" (componentEditor.ts) the standalone
// *.component.json editor, "preview" (componentPreview.ts) the live render
// of a {Name}.svelte beside its text editor. They're different enough in
// shape (many equipment + pipes vs. one component's dots vs. one component
// and its props) to stay separate Svelte trees, but small enough, and
// share enough (registry.ts, portsGestures.ts, UserIsland, theme), that a
// second webview bundle/vite config would be pure overhead.
const mode = document.body.dataset.mimicMode;
const App = mode === 'component' ? ComponentApp : mode === 'preview' ? PreviewApp : MimicApp;
mount(App, { target: document.getElementById('app')! });
