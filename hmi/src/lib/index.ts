// @joyautomation/nautilus-hmi — the HMI/digital-twin component layer of
// nautilus. SvelteKit + SSE, token-themed, runtime-agnostic.

// Visual / operator components
export { default as Tank } from './components/Tank.svelte';
export { default as Gauge } from './components/Gauge.svelte';
export { default as Trend } from './components/Trend.svelte';
export { default as TrendChart } from './components/TrendChart.svelte';
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

// Driver / connection monitoring (EtherNet/IP field driver, Sparkplug B
// publisher) — feed from GET /api/drivers or frame.drivers.
export { default as ConnectionBadge } from './components/ConnectionBadge.svelte';

// Process mimic: a P&ID-style graphic as data (*.mimic.json) rendered live —
// the spatial parts of an HMI; screens/routing stay ordinary SvelteKit.
export { default as Mimic } from './components/Mimic.svelte';

// Faceplates: the popup a mimic click opens (chrome + tab bar; compose the
// content from StatusPill / NumberField / Gauge / your own markup).
export { default as Faceplate } from './components/Faceplate.svelte';
export { default as Tabs } from './components/Tabs.svelte';
export { default as DriverStatusCard } from './components/DriverStatusCard.svelte';
export { default as DriverStatusPanel } from './components/DriverStatusPanel.svelte';

// Alarms — ISA-18.2 state, active/journal views, ack/shelve, fed from the
// `alarm/` package's GET /api/alarms*, POST /api/alarms/{ack,shelve,unshelve}
// and the stream frame's `alarms` summary. contract: docs/design/alarms.md
export { default as AlarmBanner } from './components/AlarmBanner.svelte';
export { default as AlarmTable } from './components/AlarmTable.svelte';
export { default as AlarmJournal } from './components/AlarmJournal.svelte';
export { AlarmClient, createAlarmClient, shouldRefetch, PRIORITY_ORDER, PRIORITY_META, STATE_META, DEFAULT_SHELVE_TIMES_S } from './alarms.svelte.js';
export type {
	Priority,
	AlarmState,
	AlarmInstance,
	AlarmBrief,
	AlarmSummary,
	AlarmEvent,
	AlarmEventKind,
	AlarmJournalFilter,
	AlarmJournalResult,
	AlarmClientOptions,
	FrameWithAlarms
} from './alarms.svelte.js';

// App-shell primitives: icons, menus, navigation, dialogs, toasts — the
// chrome around the process graphics, themed as siblings of the components
// above rather than a bolted-on UI library.
export { default as Icon } from './components/Icon.svelte';
export { icons } from './icons.js';
export type { IconDef, IconName } from './icons.js';
export { default as Button } from './components/Button.svelte';
export { default as MenuBar } from './components/MenuBar.svelte';
export { default as DropdownMenu } from './components/DropdownMenu.svelte';
export type { MenuItem, MenuBarMenu } from './menu.js';
export { default as Nav } from './components/Nav.svelte';
export { default as Modal } from './components/Modal.svelte';
export { default as Toast } from './components/Toast.svelte';
export { toast } from './toast.svelte.js';
export type { ToastEntry } from './toast.svelte.js';
export { default as Tooltip } from './components/Tooltip.svelte';
export { default as AppShell } from './components/AppShell.svelte';

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
export {
	pointsToPath,
	resolveBindings,
	orthogonalPoints,
	routedPoints,
	inferPortDir,
	resolvedPortDir,
	resolveRuntimePorts,
	resolvePipeEndpoints,
	portAbsolute,
	makeGetPort,
	BUILTIN_PORTS,
	PORT_STUB
} from './mimic.js';
export type {
	MimicDoc,
	MimicEquipment,
	MimicPipe,
	MimicLabel,
	MimicPort,
	MimicPipeAnchor,
	PortDir,
	EquipmentBox,
	ResolvedPort,
	GetPort,
	PipeEndpoints
} from './mimic.js';
export type {
	TrendPoint,
	TrendPen,
	TrendThreshold,
	StatusKind,
	ScanStats,
	NautilusFrame,
	TagMeta,
	ControllerMeta,
	DriverStatus,
	DriverState,
	DriverMetric,
	DriverDevice,
	NavItem,
	NavSection
} from './types.js';

// Theme tokens live in ./theme.css — import it once in your app:
//   import '@joyautomation/nautilus-hmi/theme.css';
