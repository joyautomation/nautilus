// The office cluster as one plant: its topology (three servers, three
// switches, every cable between exact ports) and each device's chassis
// profile. Every part's tags come from the controller.
import { type ChassisProfile, type Plant, type Topology } from '@joyautomation/nautilus-hmi-3d/hardware';
import sys112b from '@joyautomation/nautilus-hmi-3d/profiles/supermicro-sys-112b-wr.json';
import s3900 from '@joyautomation/nautilus-hmi-3d/profiles/fs-s3900-24t4s-r.json';
import hq from './hq.topology.json';

export const PROFILES: Record<string, ChassisProfile> = {
	[sys112b.profile]: sys112b as unknown as ChassisProfile,
	[s3900.profile]: s3900 as unknown as ChassisProfile
};
/** Switches name their model, not a profile. */
const BY_MODEL: Record<string, string> = { 'FS S3900-24T4S-R': s3900.profile };

export const topology = hq as unknown as Topology;
export const plant: Plant = {
	topology,
	profileOf: (d) => PROFILES[d.profile ?? BY_MODEL[d.model ?? ''] ?? '']
};

/** A subscription for every device in the plant: `SW1`, `SW1_*`… — the
 * controller takes at most 40 patterns, and a rack's parts are hundreds. */
export const plantPatterns = (): string[] => topology.devices.flatMap((d) => [d.tag, `${d.tag}_*`]);
