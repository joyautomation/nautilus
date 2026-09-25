import { mount } from "svelte";
import "./theme.css";
import App from "./App.svelte";
import { installKeyForward } from "./keyForward";
import { vscode } from "./vscodeApi";

// Before the app mounts, so its capture listener runs first.
installKeyForward((msg) => vscode.postMessage(msg));

mount(App, { target: document.getElementById("app")! });
