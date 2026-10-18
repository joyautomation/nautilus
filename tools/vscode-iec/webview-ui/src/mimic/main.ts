import { mount } from 'svelte';
import '../theme.css';
import './mimic.css';
import MimicApp from './MimicApp.svelte';
import PreviewApp from './PreviewApp.svelte';

// One bundle, two panes: the extension host stamps which one opened it
// onto <body data-mimic-mode> — "mimic" (mimicEditor.ts) mounts the full
// P&ID editor, "preview" (componentPreview.ts) one component's live render
// with its sample props and ports (beside its .svelte, or from the kit for
// a built-in — the editor for a {Name}.component.json). They're different
// enough in shape (many equipment + pipes vs. one component) to stay
// separate Svelte trees, but small enough, and share enough (registry.ts,
// portsGestures.ts, UserIsland, theme), that a second webview bundle/vite
// config would be pure overhead.
const mode = document.body.dataset.mimicMode;
const App = mode === 'preview' ? PreviewApp : MimicApp;
mount(App, { target: document.getElementById('app')! });
