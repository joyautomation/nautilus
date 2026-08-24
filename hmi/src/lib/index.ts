// @joyautomation/nautilus-hmi — the HMI/digital-twin component layer of
// nautilus. SvelteKit + SSE, token-themed, runtime-agnostic.

// Visual / operator components
export { default as Tank } from './components/Tank.svelte';
export { default as LevelTank } from './components/LevelTank.svelte';
export { default as Gauge } from './components/Gauge.svelte';
export { default as ScaleBar } from './components/ScaleBar.svelte';
export type { ScaleBand } from './components/ScaleBar.svelte';
export { default as Trend } from './components/Trend.svelte';
export { default as TrendChart } from './components/TrendChart.svelte';
export { default as Pump } from './components/Pump.svelte';
export { default as Valve } from './components/Valve.svelte';
export { default as Pipe } from './components/Pipe.svelte';
export { default as StatusPill } from './components/StatusPill.svelte';
export { default as Sparkline } from './components/Sparkline.svelte';
export { sparklineDomain, sparklineMetrics, sparklineGeometry } from './sparkline.js';
export type {
	SparklineDomain,
	SparklineMetrics,
	SparklinePoint,
	SparklineGeometry,
	SparklineGeometryOptions
} from './sparkline.js';
export { isThresholdActive, resolveYLabel } from './trendchart.js';
export { default as Histogram } from './components/Histogram.svelte';
export { default as NumberField } from './components/NumberField.svelte';
export { default as ScanDiagnostics } from './components/ScanDiagnostics.svelte';
export { default as ThemeSwitch } from './components/ThemeSwitch.svelte';
export { default as MotionSwitch } from './components/MotionSwitch.svelte';

// Driver / connection monitoring (EtherNet/IP field driver, Sparkplug B
// publisher) — feed from GET /api/drivers or frame.drivers.
export { default as ConnectionBadge } from './components/ConnectionBadge.svelte';

// Legacy-symbol chrome: a raster symbol library (Ignition, FactoryTalk, WinCC)
// with SCADA state decoration around it, and the one-line device status bar
// those packages tile by the hundred.
export { default as EquipSymbol } from './components/EquipSymbol.svelte';
export { default as StatusRow } from './components/StatusRow.svelte';
export type { RowState } from './components/StatusRow.svelte';

// Schematic glyphs — SVG `<g>` primitives for a network/P&ID view you lay out
// yourself. Drop them inside your own `<svg>`.
export { default as TankGlyph } from './components/TankGlyph.svelte';
export { default as PumpGlyph } from './components/PumpGlyph.svelte';
export { default as ValveGlyph } from './components/ValveGlyph.svelte';
export { default as FlowLink } from './components/FlowLink.svelte';

// Process mimic: a P&ID-style graphic as data (*.mimic.json) rendered live —
// the spatial parts of an HMI; screens/routing stay ordinary SvelteKit.
export { default as Mimic } from './components/Mimic.svelte';

// Coordinate canvas: a fixed, scaled plane of absolutely-placed symbols — the
// shape every legacy HMI screen has. Bring your own spec and component
// registry; see ./canvas.ts.
export { default as CoordinateCanvas } from './components/CoordinateCanvas.svelte';
export { CONTAINER_TYPES, inlineStyle, flexStyle, walkSpec } from './canvas.js';
export type { CanvasSpec, CanvasNode, CanvasRegistry } from './canvas.js';

// Write-back controls. All three take an injected `write(name, value)` that
// resolves to `null` on success or a refusal reason — `RealtimeClient.writeTag`
// satisfies it directly, and so does any other transport.
export { default as WriteNumber } from './components/WriteNumber.svelte';
export { default as WriteToggle } from './components/WriteToggle.svelte';
export { default as CommandButton } from './components/CommandButton.svelte';

// Faceplates: the popup a mimic click opens (chrome + tab bar; compose the
// content from StatusPill / NumberField / Gauge / your own markup).
export { default as Faceplate } from './components/Faceplate.svelte';
export { default as Tabs } from './components/Tabs.svelte';

// `FaceplateShell` is the STANDARD faceplate container — one layout for every
// equipment family (header with quality chip · state strip · hero · tabs incl.
// a standard Sim tab · footer action row), hosted either as a modal or as a
// full page from one prop. `Faceplate` remains the bare chrome underneath it.
export { default as FaceplateShell } from './components/FaceplateShell.svelte';

// The card a schematic becomes when the screen is too narrow to hold it: one
// tap target per piece of equipment, quality carried on the border.
export { default as EquipmentCard } from './components/EquipmentCard.svelte';
export type { CardValue, CardChip } from './components/EquipmentCard.svelte';

// Quality-aware value primitives. `ValueText` is the one place a number an
// operator reads gets rendered, and the one place it gets qualified.
export { default as ValueText } from './components/ValueText.svelte';
export { default as StateChip } from './components/StateChip.svelte';
export type { ChipKind } from './components/StateChip.svelte';
export {
	STATUS_META,
	valueStatus,
	isWritable,
	formatValue,
	formatAge
} from './quality.js';
export type { ValueStatus, StatusMeta, StatusInput, FormatOptions, FormattedValue } from './quality.js';
export { default as DriverStatusCard } from './components/DriverStatusCard.svelte';
export { default as DriverStatusPanel } from './components/DriverStatusPanel.svelte';

// Alarms — ISA-18.2 state, active/journal views, ack/shelve, fed from the
// `alarm/` package's GET /api/alarms*, POST /api/alarms/{ack,shelve,unshelve}
// and the stream frame's `alarms` summary. contract: docs/design/alarms.md
export { default as AlarmBanner } from './components/AlarmBanner.svelte';
export { default as AlarmTable } from './components/AlarmTable.svelte';
export { default as AlarmJournal } from './components/AlarmJournal.svelte';
export { default as AckButton, worstFirst, ackLine } from './components/AckButton.svelte';
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

// Confirmation: one dialog, mounted once, for every irreversible or
// plant-affecting action. `confirm()` is promise-based, so a call site reads
// `if (await confirm({…})) …` and cannot forget to ask.
export { default as ConfirmDialog } from './components/ConfirmDialog.svelte';
export { confirm, confirmState } from './confirm.svelte.js';
export { createConfirmQueue, splitItems, getOperator, setOperator, DEFAULT_MAX_ITEMS } from './confirm.js';
export type { ConfirmOptions, ConfirmRequest, ConfirmQueue, QueueOptions } from './confirm.js';
export { default as Tooltip } from './components/Tooltip.svelte';
export { default as AppShell } from './components/AppShell.svelte';

// Realtime client (generic over the frame shape)
export { RealtimeClient, createRealtimeClient, TrendBuffer } from './realtime.svelte.js';
export type { RealtimeOptions, FrameSource } from './realtime.svelte.js';

// Trends: live off the stream, backfilled from the historian.
export { useTrend } from './trend.svelte.js';
export type { UseTrendOptions } from './trend.svelte.js';
export { fetchHistory, historyAvailable, mergeHistory } from './history.js';
export type { HistoryOptions, HistoryResult } from './history.js';

// Reading a frame's tag map — dotted struct paths, and enumerating what a
// runtime actually publishes (for a tag picker that can't offer a dead tag).
export { tagAt, numAt, boolAt, hasTagAt, numericLeaves } from './tags.js';
export type { TagTree, NumericLeaf } from './tags.js';
// Declaring a SUBSET of them: packing a screen's tag list into the handful of
// glob patterns one `?tags=` subscription accepts.
export {
	MAX_TAG_PATTERNS,
	NO_TAGS,
	mergeTagPatterns,
	packTagPatterns,
	tagPatternMatches,
	tagInPatterns
} from './tags.js';

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
	Quality,
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
// The house faces (Space Grotesk / IBM Plex Mono / Righteous), self-hosted via
// @fontsource, are the optional sibling — theme.css only NAMES the families:
//   import '@joyautomation/nautilus-hmi/fonts.css';
