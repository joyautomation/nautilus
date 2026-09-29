// @joyautomation/nautilus-hmi-3d/hardware — servers from chassis profiles
// (docs/design/spatial-hmi.md §3e). A separate entry point: it carries the
// glTF loader for the part library, which the base bundle must not.
export { default as Server } from './Server.svelte';
export { default as Chassis } from './Chassis.svelte';
export { default as Drive } from './Drive.svelte';
export { default as Dimm } from './Dimm.svelte';
export { default as PcieCard } from './PcieCard.svelte';
export { default as Fan } from './Fan.svelte';
export { default as Psu } from './Psu.svelte';
export { default as Cpu } from './Cpu.svelte';
export { default as Port } from './Port.svelte';
export { default as PartModel, loadPart } from './PartModel.svelte';
export { default as PartFaceplate } from './PartFaceplate.svelte';
export {
	PART_KINDS,
	validateProfile,
	resolveParts,
	serverTags,
	tagFor,
	anchorPayload,
	partState,
	partStatus,
	partFacts,
	serverFacts,
	isFitted,
	capacity,
	mm,
	HEALTH_TEXT
} from './profile.js';
export type { ChassisProfile, ProfilePart, ProfileAnchor, ServerPart, PartKind, PartState, Fact } from './profile.js';
export { partLook, DEFAULT_MODELS } from './look.js';
export { OVERLAYS, heatOverlay, interfacesOverlay, freeOverlay, heatPaint, limitsFor, inletC, nextDimms, placeLabels, overlayColors, HEAT_RAMP } from './overlay.js';
export type { Overlay, OverlayContext, OverlayColors, PartPaint, LegendItem } from './overlay.js';
export type { PartLook } from './look.js';
export type { PartProps } from './types.js';
