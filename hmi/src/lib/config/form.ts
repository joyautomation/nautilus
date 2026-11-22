// What the config screens edit, as data (#711): the manifest's driver and
// device rows read from its text, the edits a form makes as text patches,
// and the overlay they add up to. The overlay is what slice 3 hands to the
// controller's staged routes ({files, driver}); nothing here writes.

import { parse } from 'yaml';
import { patch, type Op, type Path } from './yamlpatch.js';
import type { Field, Json } from './schema.js';

export type Row = { [k: string]: Json };

export interface RowRef {
	/** Where the row is in the manifest: ['driver'] or ['drivers', 2] or ['devices', 0]. */
	path: Path;
	value: Row;
}

function doc(text: string): { [k: string]: Json } {
	const v = parse(text) as unknown;
	return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as { [k: string]: Json }) : {};
}

/** The driver rows: `driver:` is one row, `drivers:` a list. Both at once is the loader's error, not an edit. */
export function driverRows(manifest: string): RowRef[] {
	const d = doc(manifest);
	if (d.driver !== undefined && d.drivers !== undefined) throw new Error('the manifest sets both driver: and drivers:; the loader refuses that, so fix the manifest first');
	if (d.driver && typeof d.driver === 'object' && !Array.isArray(d.driver)) return [{ path: ['driver'], value: d.driver as Row }];
	if (Array.isArray(d.drivers)) return d.drivers.map((v, i) => ({ path: ['drivers', i], value: v as Row }));
	return [];
}

/** The `devices:` rows. */
export function deviceRows(manifest: string): RowRef[] {
	const d = doc(manifest);
	return Array.isArray(d.devices) ? d.devices.map((v, i) => ({ path: ['devices', i], value: v as Row })) : [];
}

/** The ops for one field of a row: a value sets it, undefined clears it. */
export function setField(row: Path, key: string, value: Json | undefined): Op {
	return value === undefined ? { op: 'delete', path: [...row, key] } : { op: 'set', path: [...row, key], value };
}

/**
 * Add a driver row. A manifest that has `driver:` (one row) cannot take a
 * second one without being rewritten into `drivers:`, which this does not
 * do: it says so and leaves the manifest alone.
 */
export function addDriver(manifest: string, row: Row): Op {
	const d = doc(manifest);
	if (d.driver !== undefined) throw new Error('this manifest sets driver: (one driver); to add another, change it to a drivers: list in nautilus.yaml first');
	return { op: 'append', path: ['drivers'], value: row };
}

export function addDevice(row: Row): Op {
	return { op: 'append', path: ['devices'], value: row };
}

/** Remove a row. Removing the last driver is refused: a project needs one. */
export function removeRow(manifest: string, ref: RowRef): Op {
	if (ref.path[0] === 'driver') throw new Error('cannot remove the only driver: a project needs one');
	if (ref.path[0] === 'drivers' && driverRows(manifest).length === 1) throw new Error('cannot remove the last driver: a project needs one');
	return { op: 'delete', path: ref.path };
}

/** The overlay a set of edits makes: only the files whose text changed. */
export interface Overlay {
	files: Record<string, string>;
	/** The paths changed, sorted: what a diff view lists. */
	changed: string[];
}

export function overlay(files: Record<string, string>, edits: Record<string, Op[]>): Overlay {
	const out: Record<string, string> = {};
	for (const [path, ops] of Object.entries(edits)) {
		const was = files[path];
		if (was === undefined) throw new Error(`${path} is not one of the files the controller sent`);
		const now = patch(was, ops);
		if (now !== was) out[path] = now;
	}
	return { files: out, changed: Object.keys(out).sort() };
}

/**
 * The driver types a setting applies to, read from its description, which by
 * the schema's convention opens with them: `eip: …`, `eip / modbus: …`, and
 * further sentences `… opcua-client: …`. null for a setting no description
 * names (`type`, `name`): it applies to every type. `known` is the type enum,
 * so a sentence that merely contains a colon is not read as a type list.
 */
export function appliesTo(f: Field, known: string[]): string[] | null {
	const d = f.description ?? '';
	const out = new Set<string>();
	for (const m of d.matchAll(/(?:^|[.;]\s+)([a-z][a-z0-9-]*(?:\s*\/\s*[a-z][a-z0-9-]*)*):\s/g)) {
		for (const t of m[1].split('/').map((x) => x.trim())) if (known.includes(t)) out.add(t);
	}
	return out.size ? [...out] : null;
}

/**
 * The settings of a driver row worth showing: those of its type, those no
 * type owns, and any the row already sets (a stray one stays visible so it
 * can be seen and removed). A modbus row does not offer host, slot, broker.
 */
export function fieldsForType(driver: Field, type: string | undefined, value: Row | undefined): Field[] {
	const all = driver.fields ?? [];
	const known = all.find((f) => f.key === 'type')?.enum ?? [];
	if (!type || !known.includes(type)) return all;
	return all.filter((f) => {
		if (value?.[f.key] !== undefined) return true;
		const t = appliesTo(f, known);
		return t === null || t.includes(type);
	});
}
