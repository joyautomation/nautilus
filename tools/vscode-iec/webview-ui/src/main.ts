import { mount } from "svelte";
import "./theme.css";
import App from "./App.svelte";
import { installKeyForward } from "./keyForward";
import { installXref } from "./xref.svelte";
import { vscode } from "./vscodeApi";

// Before the app mounts, so its capture listener runs first.
installKeyForward((msg) => vscode.postMessage(msg));
// Shift+F12 / right-click "Find All References" on an element, and the
// tag descriptions its tooltip shows (#218, #216) — all three views.
installXref((msg) => vscode.postMessage(msg));

mount(App, { target: document.getElementById("app")! });
