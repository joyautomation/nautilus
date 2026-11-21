// The Modbus points editor's data (#711, slice 5a): a driver's modbus
// manifest (sources and tag bindings) read from its text, and the small
// text patches an edit makes. Only what a live apply may change is
// editable (internal/project/ioconfig.go: liveSource host/port/unitid,
// liveModbus table/address/scanclass); every other column is shown, not
// edited, and a tag cannot be added or removed here because the tag set is
// fixed while the runtime runs.

import { parse } from 'yaml';
import type { Json } from './schema.js';
import { driverRows, type Row } from './form.js';
import type { Op } from './yamlpatch.js';

/** The reserved scan class that keeps a tag in the catalog without polling it. */
export const NO_POLL = 'none';
export const TABLES = ['holding', 'input', 'coil', 'discrete'] as const;

export interface SourceRow {
	index: number;
	id: string;
	host: string;
	port?: number;
	unitid?: number;
	rest: Row;
}
export interface TagRow {
	index: number;
	name: string;
	source: string;
	table: string;
	address?: number;
	scanclass?: string;
	format?: string;
	writable: boolean;
	rest: Row;
}
export interface ModbusManifest {
	sources: SourceRow[];
	tags: TagRow[];
}

const obj = (v: unknown): v is Row => v !== null && typeof v === 'object' && !Array.isArray(v);
const num = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined);

/** The sources and tag bindings in a modbus manifest's text; a malformed entry is skipped, not guessed at. */
export function parseModbus(text: string): ModbusManifest {
	const d = parse(text) as unknown;
	const doc: Row = obj(d) ? d : {};
	const sources: SourceRow[] = [];
	const tags: TagRow[] = [];
	if (Array.isArray(doc.sources))
		doc.sources.forEach((v, index) => {
			if (!obj(v)) return;
			sources.push({ index, id: String(v.id ?? ''), host: String(v.host ?? ''), port: num(v.port), unitid: num(v.unitid), rest: v });
		});
	if (Array.isArray(doc.tags))
		doc.tags.forEach((v, index) => {
			if (!obj(v)) return;
			tags.push({
				index,
				name: String(v.name ?? ''),
				source: String(v.source ?? ''),
				table: String(v.table ?? ''),
				address: num(v.address),
				scanclass: typeof v.scanclass === 'string' ? v.scanclass : undefined,
				format: typeof v.format === 'string' ? v.format : undefined,
				writable: v.writable === true,
				rest: v
			});
		});
	return { sources, tags };
}

/** The modbus manifests of the project: the files a modbus driver names that the controller sent. */
export function modbusManifests(files: Record<string, string>, manifest: string): { path: string; driver: string; classes: string[] }[] {
	let rows;
	try {
		rows = driverRows(files[manifest] ?? '');
	} catch {
		return [];
	}
	const out: { path: string; driver: string; classes: string[] }[] = [];
	const seen = new Set<string>();
	for (const r of rows) {
		const p = r.value.manifest;
		if (r.value.type !== 'modbus' || typeof p !== 'string' || seen.has(p) || files[p] === undefined) continue;
		seen.add(p);
		const sc = r.value['scan-classes'];
		out.push({ path: p, driver: String(r.value.name ?? 'modbus'), classes: obj(sc) ? Object.keys(sc) : [] });
	}
	return out;
}

export type SourceField = 'host' | 'port' | 'unitid';
export type TagField = 'table' | 'address' | 'scanclass';

/** Edit one field of a source. An empty port or unit id clears it (the default). */
export function setSourceField(index: number, key: SourceField, value: Json | undefined): Op {
	return value === undefined ? { op: 'delete', path: ['sources', index, key] } : { op: 'set', path: ['sources', index, key], value };
}

/** Edit one field of a tag binding. An empty scan class clears it (the default class). */
export function setTagField(index: number, key: TagField, value: Json | undefined): Op {
	return value === undefined ? { op: 'delete', path: ['tags', index, key] } : { op: 'set', path: ['tags', index, key], value };
}

export interface Problem {
	where: string;
	message: string;
}

/** What is wrong before the controller is asked; its validate stays the authority. */
export function problems(m: ModbusManifest, classes: string[] = []): Problem[] {
	// The default class (empty) and the reserved `none` always exist; any other must be a scan-classes: entry of the driver.
	const declared = new Set([...classes, NO_POLL]);
	const out: Problem[] = [];
	const ids = new Set(m.sources.map((s) => s.id));
	for (const s of m.sources) {
		const at = `source ${s.id}`;
		if (!s.host.trim()) out.push({ where: at, message: `${at} needs a host` });
		if (s.port !== undefined && !(Number.isInteger(s.port) && s.port >= 0 && s.port <= 65535)) out.push({ where: at, message: `${at}: port must be a whole number from 0 to 65535 (0 or empty means 502)` });
		if (s.unitid !== undefined && !(Number.isInteger(s.unitid) && s.unitid >= 0 && s.unitid <= 255)) out.push({ where: at, message: `${at}: unit id must be a whole number from 0 to 255` });
	}
	for (const t of m.tags) {
		const at = `tag ${t.name}`;
		if (!(TABLES as readonly string[]).includes(t.table)) out.push({ where: at, message: `${at}: table must be one of ${TABLES.join(', ')}` });
		if (t.address === undefined || !Number.isInteger(t.address) || t.address < 0 || t.address > 65535) out.push({ where: at, message: `${at}: address must be a whole number from 0 to 65535` });
		if (t.writable && (t.table === 'input' || t.table === 'discrete')) out.push({ where: at, message: `${at} is writable, and only the holding and coil tables can be written: change the table back, or the tag is read-only in the manifest` });
		if ((t.table === 'coil' || t.table === 'discrete') && t.format !== undefined && t.format !== 'bool') out.push({ where: at, message: `${at}: a ${t.table} table holds bits, so its format must be bool` });
		if (t.scanclass && !declared.has(t.scanclass)) out.push({ where: at, message: `${at}: scan class ${t.scanclass} is not declared; add it under scan-classes: on the driver in nautilus.yaml, or pick one of ${[...declared].join(', ')}` });
		if (!ids.has(t.source)) out.push({ where: at, message: `${at} names source ${t.source}, which the manifest does not have` });
	}
	return out;
}
