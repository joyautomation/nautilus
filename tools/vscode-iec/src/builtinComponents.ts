// Host-side mirror of webview-ui/src/mimic/builtinPorts.ts, which is the
// SOURCE OF TRUTH — the extension host can't import that module (or
// registry.ts) directly since it pulls in the real Svelte components via
// @joyautomation/nautilus-hmi, a browser-only dependency chain the host
// bundle has no business loading. This file is deliberately tiny (name +
// default ports only) and kept in manual sync with the webview copy;
// changing a built-in's default ports means editing BOTH files (the kit's
// BUILTIN_PORTS and this), and builtinComponents.test.ts fails until they
// agree — as it does for the component set and DEMO_PROPS.
//
// Used by the "Edit Component Ports…" command (editComponentPorts.ts) for
// its QuickPick list, by the component preview (componentPreview.ts) to
// show a built-in's default ports until a sidecar overrides them, and by
// "Copy Kit Component…" (copyKitComponent.ts) to seed the copy's sidecar.
import type { Port } from "./mimicComponentIndex";

export const BUILTIN_COMPONENT_PORTS: Record<string, Port[]> = {
  // BUILTIN_PORTS in hmi/src/lib/mimic.ts, verbatim: each default port is
  // anchored to the `data-part` of the drawing a pipe meets, as fractions
  // of that part, so it stays on the vessel wall, the valve body's ends or
  // a nozzle's mouth at any size; inferPortDir() reads the exit direction
  // off the fraction. builtinComponents.test.ts holds the copy to the kit.
  Tank: [
    { name: "top", part: "vessel", x: 0.5, y: 0 },
    { name: "left", part: "vessel", x: 0, y: 0.5 },
    { name: "right", part: "vessel", x: 1, y: 0.5 },
    { name: "bottom", part: "vessel", x: 0.5, y: 1 },
  ],
  Heater: [],
  Pump: [
    { name: "in", part: "suction", x: 0, y: 0.5 },
    { name: "out", part: "discharge", x: 0.5, y: 0 },
  ],
  Valve: [
    { name: "in", part: "body", x: 0, y: 0.5 },
    { name: "out", part: "body", x: 1, y: 0.5 },
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
  Tank: { levelPct: 62 },
  Heater: { powerPct: 60 },
  Pump: { running: true },
  Valve: { openPct: 70 },
  Gauge: { value: 65, setpoint: 65 },
};
