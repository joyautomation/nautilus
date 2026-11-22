// The asset page's choices (#714 slice 1): what it trends, which alarms are
// its own, what it offers to audit, and its slug.
import { describe, it, expect } from './harness.js';
import { areaRows, assetGroups, hasTag, tagValue, assetAlarms, assetFromEquipment, assetSlug, assetTags, assetTitle, indexAssets, trendTags, writableTags, type AssetDescriptor } from '../src/lib/asset.js';
import type { AlarmInstance } from '../src/lib/alarms.svelte.js';

const pump: AssetDescriptor = {
	id: 'P101',
	panel: {
		title: 'P-101 transfer pump',
		readings: [
			{ tag: 'P101.Run', on: 'Running', off: 'Stopped' },
			{ tag: 'P101.Flow', unit: 'gpm' },
			{ tag: 'P101.Amps', unit: 'A' },
			{ tag: 'P101.Trip', alarm: true, decimals: 0 },
			{ tag: 'P101.State', unit: 'state', on: 'Run', off: 'Stop' },
			{ tag: 'P101.Temp', unit: 'C' },
			{ tag: 'P101.Vib', unit: 'mm/s' },
			{ tag: 'P101.Psi', unit: 'psi' }
		],
		setpoints: [{ tag: 'P101.SpeedSP' }],
		commands: [{ tag: 'P101.Start', label: 'Start', held: 'P101.Run' }, { tag: 'P101.Stop', label: 'Stop' }],
		toggles: [{ tag: 'P101.Auto', label: 'Auto' }]
	}
};
const alarm = (id: string, equipment: string | undefined, priority = 'high'): AlarmInstance =>
	({ id, name: id, tag: id, priority, equipment, state: 'unack-active', cond: true }) as unknown as AlarmInstance;

describe('what the page trends', () => {
	it('is the first few numeric readings, never a true/false', () => {
		expect(trendTags(pump).join()).toBe('P101.Flow,P101.Amps,P101.Temp,P101.Vib');
	});
	it('is the descriptor\'s own list when it has one, once each', () => {
		expect(trendTags({ ...pump, trend: ['P101.Psi', 'P101.Psi', 'P101.Flow'] }).join()).toBe('P101.Psi,P101.Flow');
		expect(trendTags({ ...pump, trend: [] }).length).toBe(0);
	});
});

describe('the tags it touches', () => {
	it('lists each once: readings, settings, commands with their readback, toggles', () => {
		const t = assetTags(pump);
		expect(t.length).toBe(new Set(t).size);
		for (const x of ['P101.Run', 'P101.Flow', 'P101.SpeedSP', 'P101.Start', 'P101.Stop', 'P101.Auto']) expect(t.includes(x)).toBe(true);
	});
	it('offers only what an operator can write to the audit picker', () => {
		expect(writableTags(pump).join()).toBe('P101.SpeedSP,P101.Start,P101.Stop,P101.Auto');
	});
});

describe('its alarms', () => {
	it('are those of its equipment key, in any case, worst first, and not the others\'', () => {
		const list = [alarm('b', 'p101', 'low'), alarm('c', 'P102'), alarm('a', 'P101', 'critical'), alarm('d', undefined)];
		expect(assetAlarms(list, pump).map((a) => a.id).join()).toBe('a,b');
	});
	it('use the equipment key when the descriptor names one, and the id too', () => {
		const list = [alarm('x', 'Skid'), alarm('y', 'P101')];
		expect(assetAlarms(list, { ...pump, equipment: 'Skid' }).map((a) => a.id).join()).toBe('x,y');
		expect(assetAlarms(list, pump).map((a) => a.id).join()).toBe('y');
	});
});

describe('titles, slugs and the index', () => {
	it('title falls back to the panel\'s, then the id', () => {
		expect(assetTitle({ ...pump, title: 'Mine' })).toBe('Mine');
		expect(assetTitle(pump)).toBe('P-101 transfer pump');
		expect(assetTitle({ id: 'X', panel: {} })).toBe('X');
	});
	it('a slug is lowercase words and digits', () => {
		expect(assetSlug(' P-101 / Pump_A ')).toBe('p-101-pump-a');
		expect(assetSlug('///')).toBe('');
	});
	it('the index keeps the first of two ids with one slug and drops the empty', () => {
		const got = indexAssets([{ id: 'P-1', panel: {} }, { id: 'p 1', panel: {} }, { id: '!!', panel: {} }, { id: 'T1', panel: {} }]);
		expect(got.map((g) => g.slug + ':' + g.asset.id).join()).toBe('p-1:P-1,t1:T1');
	});
});

describe('from a mimic\'s equipment', () => {
	it('takes its panel, or the panel of the equipment it shares, and none when it has none', () => {
		const panels = (id: string) => (id === 'WW' ? { title: 'Wet well', readings: [{ tag: 'L' }] } : undefined);
		expect(assetFromEquipment({ id: 'FS', panel: 'WW' }, panels)?.title).toBe('Wet well');
		expect(assetFromEquipment({ id: 'P', label: 'Pump', panel: { readings: [] } }, panels)?.title).toBe('Pump');
		expect(assetFromEquipment({ id: 'V' }, panels)).toBe(null);
		expect(assetFromEquipment({ id: 'F', panel: 'Nowhere' }, panels)).toBe(null);
	});
});

describe('a value by path', () => {
	it('is the tag when the frame has it, else the member of a struct tag, else nothing', () => {
		const tags = { 'P1.Flow': 7, CONV: { Speed: 120, Drive: { Amps: 3 } }, Running: false };
		expect(tagValue(tags, 'P1.Flow')).toBe(7);
		expect(tagValue(tags, 'CONV.Speed')).toBe(120);
		expect(tagValue(tags, 'CONV.Drive.Amps')).toBe(3);
		expect(tagValue(tags, 'Running')).toBe(false);
		expect(tagValue(tags, 'CONV.Nope')).toBe(undefined);
		expect(tagValue(tags, 'Running.X')).toBe(undefined);
		expect(hasTag(tags, 'Running')).toBe(true);
		expect(hasTag(tags, 'CONV.Nope')).toBe(false);
	});
});

describe('the index by area', () => {
	it('groups by area in the order first seen, the unplaced last, each in the order given', () => {
		const a = (id: string, area?: string): AssetDescriptor => ({ id, area, panel: {} });
		const g = assetGroups([a('A'), a('B', 'Roast'), a('C', 'Receiving'), a('D', 'Roast')]);
		expect(g.map((x) => x.area + ':' + x.assets.map((y) => y.id).join('')).join('|')).toBe('Roast:BD|Receiving:C|:A');
		expect(assetGroups([]).length).toBe(0);
	});
});

describe('an area page\'s rows', () => {
	const mk = (id: string, equipment: string): AssetDescriptor => ({ id, equipment, panel: {} });
	const al = (id: string, equipment: string, priority: string, state: string): AlarmInstance =>
		({ id, name: id, tag: id, priority, equipment, state, cond: true }) as unknown as AlarmInstance;
	it('lists the area\'s assets in its order with the alarms standing on each, the worst first, suppressed ones counted apart', () => {
		const assets = [mk('P1', 'P1'), mk('P2', 'P2'), mk('T1', 'T1')];
		const inst = [
			al('a', 'P1', 'low', 'unack-active'),
			al('b', 'P1', 'high', 'ack-active'),
			al('c', 'P1', 'critical', 'suppressed'),
			al('d', 'T1', 'medium', 'unack-rtn'),
			al('e', 'P2', 'high', 'normal')
		];
		const rows = areaRows({ id: 'A', assets: ['T1', 'Gone', 'p1', 'P2'] }, assets, inst);
		expect(rows.map((r) => r.asset.id).join()).toBe('T1,P1,P2');
		const [t1, p1, p2] = rows;
		expect([t1.unacked, t1.active, t1.worst].join()).toBe('1,1,medium');
		expect([p1.unacked, p1.active, p1.suppressed, p1.worst].join()).toBe('1,2,1,high');
		expect([p2.active, p2.worst === '' ? 'none' : p2.worst].join()).toBe('0,none');
	});
});
