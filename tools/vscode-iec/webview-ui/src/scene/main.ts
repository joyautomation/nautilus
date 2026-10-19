import { mount } from 'svelte';
import '../theme.css';
import '../mimic/mimic.css';
import SceneApp from './SceneApp.svelte';

// The scene editor bundle (sceneEditor.ts opens it): its own file, apart
// from mimic-editor.js, because three + Threlte are most of a megabyte
// that no P&ID needs. mimic.css is imported for its token bridge: the
// renderer reads the kit's theme tokens (--crit, --warn, --ink-2, …) off
// the DOM for its palette, and the bridge maps them onto VS Code's theme.
mount(SceneApp, { target: document.getElementById('app')! });
