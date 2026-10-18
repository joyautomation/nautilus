// Host-side mirror of webview-ui/src/mimic/builtinPorts.ts, which is the
// SOURCE OF TRUTH — the extension host can't import that module (or
// registry.ts) directly since it pulls in the real Svelte components via
// @joyautomation/nautilus-hmi, a browser-only dependency chain the host
// bundle has no business loading. This file is deliberately tiny (name +
// default ports only) and kept in manual sync with the webview copy;
// changing a built-in's default ports means editing BOTH files.
//
// Used by the "Edit Component Ports…" command (editComponentPorts.ts) for
// its QuickPick list, by the component preview (componentPreview.ts) to
// show a built-in's default ports until a sidecar overrides them, and by
// "Copy Kit Component…" (copyKitComponent.ts) to seed the copy's sidecar.
import type { Port } from "./mimicComponentIndex";

export const BUILTIN_COMPONENT_PORTS: Record<string, Port[]> = {
  // Measured off each drawing — see BUILTIN_PORTS in hmi/src/lib/mimic.ts.
  Tank: [
    { name: "top", x: 0.5, y: 0.077, dir: "up" },
    { name: "left", x: 0.155, y: 0.465, dir: "left" },
    { name: "right", x: 0.845, y: 0.465, dir: "right" },
    { name: "bottom", x: 0.5, y: 0.854, dir: "down" },
  ],
  Pump: [
    { name: "in", x: 0, y: 0.527, dir: "left" },
    { name: "out", x: 0.483, y: 0.055, dir: "up" },
  ],
  Valve: [
    { name: "in", x: 0.133, y: 0.55, dir: "left" },
    { name: "out", x: 0.867, y: 0.55, dir: "right" },
  ],
  Gauge: [],
  Sparkline: [],
};

export const BUILTIN_COMPONENT_NAMES = Object.keys(BUILTIN_COMPONENT_PORTS);

/** Mirror of registry.ts's DEMO_PROPS (same manual-sync rule as the ports
 * above): the sample props a copy of a built-in starts with, so its
 * preview shows the component mid-motion rather than empty. */
export const BUILTIN_DEMO_PROPS: Record<string, Record<string, unknown>> = {
  Sparkline: { values: [4, 6, 5, 8, 6, 9, 7, 8] },
  Tank: { levelPct: 62, tempC: 65 },
  Pump: { running: true },
  Valve: { openPct: 70 },
  Gauge: { value: 65, setpoint: 65 },
};
