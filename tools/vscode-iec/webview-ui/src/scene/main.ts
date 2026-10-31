import { mount } from 'svelte';
import '../theme.css';
import '../mimic/mimic.css';
import SceneApp from './SceneApp.svelte';
import KindApp from './KindApp.svelte';

// The scene editor bundle (sceneEditor.ts opens it): its own file, apart
// from mimic-editor.js, because three + Threlte are most of a megabyte
// that no P&ID needs. mimic.css is imported for its token bridge: the
// renderer reads the kit's theme tokens (--crit, --warn, --ink-2, …) off
// the DOM for its palette, and the bridge maps them onto VS Code's theme.
//
// One bundle, two editors: the host stamps <body data-scene-mode> —
// "kind" mounts the kind editor (kindEditor.ts: one *.kind.json on a
// turntable), anything else the scene editor — the way the mimic bundle
// hosts the component editor.
const mode = document.body.dataset.sceneMode === 'kind' ? KindApp : SceneApp;
mount(mode, { target: document.getElementById('app')! });
