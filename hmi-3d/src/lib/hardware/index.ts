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
export { default as Rack } from './Rack.svelte';
export { default as SlideRails } from './SlideRails.svelte';
export { default as Cable } from './Cable.svelte';
export { default as NetworkMesh } from './NetworkMesh.svelte';
export {
	PART_KINDS,
	validateProfile,
	resolveParts,
	serverTags,
	tagFor,
	anchorPayload,
	partState,
	partStatus,
	portReading,
	partFacts,
	serverFacts,
	switchFacts,
	isFitted,
	capacity,
	mm,
	partForSensor,
	faceHoles,
	HEALTH_TEXT
} from './profile.js';
export type { ChassisProfile, ProfilePart, ProfileAnchor, ServerPart, PartKind, PartState, Fact, FaceHole } from './profile.js';
export { partLook, DEFAULT_MODELS } from './look.js';
export { OVERLAYS, identifyOverlay, identify, KIND_COLORS, cablesOverlay, linkOnPort, verdictColor, vlanOverlay, vlansOnPort, vlanVerdictColor, heatOverlay, interfacesOverlay, freeOverlay, heatPaint, sensorLimits, limitsFor, inletC, nextDimms, placeLabels, fanOutLabels, overlayColors, HEAT_RAMP } from './overlay.js';
export type { Overlay, OverlayContext, OverlayColors, PartPaint, LegendItem } from './overlay.js';
export type { PartLook } from './look.js';
export type { PartProps } from './types.js';
export { checkLink, checkAll, neighbourhood, focusFade, linkTags, linkAt, linkFacts, resolveEnd, readEnd, parseEnd, portPart, portName, deviceById, deviceByTag, VERDICT_MARK } from './topology.js';
export type { Topology, TopoDevice, TopoLink, LinkVlans, TopoVlan, Plant, PortRef, End, Reading, Verdict, LinkCheck } from './topology.js';
export { RACK_U_MM, uY, placeDevice, toRack, portMouth, endInRack, cablePath, LANE_X, holeYs, EAR_X, RAIL_X, HOLE_X, EAR_T, U_HOLES } from './rack.js';
export type { RackLayout, RackDevice, Placement } from './rack.js';
export { meshLayout } from './mesh.js';
export type { MeshNode, MeshEdge } from './mesh.js';
export { parseList, portPosition, vlanTable, readPortVlans, vlanText, declaredText, checkVlans, checkAllVlans, vlanFacts, vlanColors, siteVlans, vlanDomains, islandsOf, domainDevices, vlanFloors, VLAN_MARK, VLAN_COLORS, DEFAULT_VLAN_COLOR } from './vlan.js';
export type { VlanRow, PortVlans, VlanVerdict, VlanEnd, VlanCheck, DomainLink, VlanDomain, VlanFloor } from './vlan.js';
export { default as VlanFloors } from './VlanFloors.svelte';
