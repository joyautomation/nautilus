// @joyautomation/nautilus-hmi — the HMI/digital-twin component layer of
// nautilus. SvelteKit + SSE, token-themed, runtime-agnostic.

// Visual / operator components
export { default as Tank } from './components/Tank.svelte';
export { default as Gauge } from './components/Gauge.svelte';
export { default as Trend } from './components/Trend.svelte';
export { default as Pump } from './components/Pump.svelte';
export { default as Valve } from './components/Valve.svelte';
export { default as Pipe } from './components/Pipe.svelte';
export { default as StatusPill } from './components/StatusPill.svelte';
export { default as Sparkline } from './components/Sparkline.svelte';
export { default as Histogram } from './components/Histogram.svelte';
export { default as NumberField } from './components/NumberField.svelte';
export { default as ScanDiagnostics } from './components/ScanDiagnostics.svelte';
export { default as ThemeSwitch } from './components/ThemeSwitch.svelte';
export { default as MotionSwitch } from './components/MotionSwitch.svelte';

// Realtime client (generic over the frame shape)
export { RealtimeClient, createRealtimeClient, TrendBuffer } from './realtime.svelte.js';
export type { RealtimeOptions } from './realtime.svelte.js';

// Theme / motion preference stores
export { theme } from './theme.svelte.js';
export type { Theme } from './theme.svelte.js';
export { motion } from './motion.svelte.js';
export type { Motion } from './motion.svelte.js';

// Helpers & types
export { tempColor } from './color.js';
export type {
	TrendPoint,
	StatusKind,
	ScanStats,
	NautilusFrame,
	TagMeta,
	ControllerMeta
} from './types.js';

// Theme tokens live in ./theme.css — import it once in your app:
//   import '@joyautomation/nautilus-hmi/theme.css';
