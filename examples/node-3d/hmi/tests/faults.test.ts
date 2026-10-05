// The scenario panel's and part menu's logic (src/lib/faults.ts): which
// faults a part offers, what is set now, and the catalog. `npm test` runs
// it on node, which strips the types; the state below is the plant's
// (examples/it-cluster) shape, as /plant/api/state?tags=Sim_* returns it.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { activeFaults, faultsFor, parseCatalog, simTagsFor, since, who } from '../src/lib/faults.ts';

const quiet = () => ({
	Sim_Room: { InletDeltaC: 0 },
	Sim_Net: { BroadcastPps: 0, MulticastPps: 0 },
	Sim_SW3: { Dark: false, Reboot: false },
	Sim_SW3_Port25: { AdminDown: false, Down: false, ErrorRate: 0, SpeedMbps: 0 },
	Sim_Cable_SW3_Port25: { Pulled: false },
	Sim_NODE1: { CpuLoad: 0, Dark: false, PowerOff: false },
	Sim_NODE1_Fan3: { Fail: false },
	Sim_NODE1_PSU2: { Fail: false, InputLost: false },
	Sim_NODE3_Drive_NVMe2: { Failing: false, Pulled: false },
	Sim_NODE3_Nic_Slot3_P2: { Down: false }
});
const labels = (fs: { label: string }[]) => fs.map((f) => f.label);

describe('faultsFor: what a part offers', () => {
	const s = quiet();
	it('a fan fails', () => assert.deepEqual(labels(faultsFor(s, ['Sim_NODE1_Fan3'])), ['fail']));
	it('a drive is pulled or failing', () => assert.deepEqual(labels(faultsFor(s, ['Sim_NODE3_Drive_NVMe2'])), ['pull', 'failing']));
	it('a PSU fails or loses its input', () => assert.deepEqual(labels(faultsFor(s, ['Sim_NODE1_PSU2'])), ['fail', 'lose input']));
	it('a switch port: down, admin down, 1G, errors', () =>
		assert.deepEqual(labels(faultsFor(s, ['Sim_SW3_Port25'])), ['down', 'admin down', '1G', 'errors 50/s']));
	it("a server's Dark is its BMC; a switch's is the switch", () => {
		assert.deepEqual(labels(faultsFor(s, ['Sim_NODE1'])), ['BMC dark', 'power off', 'CPU 100 %']);
		assert.deepEqual(labels(faultsFor(s, ['Sim_SW3'])), ['dark', 'reboot']);
	});
	it('a REAL member is set with a value that makes the fault, cleared with 0', () => {
		const [cpu] = faultsFor(s, ['Sim_NODE1']).filter((f) => f.member === 'CpuLoad');
		assert.equal(cpu.set, 100);
		assert.equal(cpu.clear, 0);
		const [pull] = faultsFor(s, ['Sim_NODE1_Fan3']);
		assert.equal(pull.set, true);
		assert.equal(pull.clear, false);
	});
	it('a member it does not know still shows, by its name', () => {
		const f = faultsFor({ Sim_X: { Smoke: false, Level: 0 } }, ['Sim_X']);
		assert.deepEqual(
			f.map((x) => [x.label, x.set, x.clear]),
			[
				['Smoke', true, false],
				['Level', 1, 0]
			]
		);
	});
	it('a tag the plant does not have offers nothing', () => assert.deepEqual(faultsFor(s, ['Sim_NODE9_Fan1']), []));
	it('says which are set', () => {
		const t = { ...s, Sim_NODE1_PSU2: { Fail: false, InputLost: true } };
		assert.deepEqual(
			faultsFor(t, ['Sim_NODE1_PSU2']).map((f) => [f.label, f.on]),
			[
				['fail', false],
				['lose input', true]
			]
		);
	});
});

describe('simTagsFor: the structs a right-click reads', () => {
	it('a port: its own, its cable by either end, then its device', () =>
		assert.deepEqual(simTagsFor('NODE3_Nic_Slot3_P2', ['SW3_Port25', 'NODE3_Nic_Slot3_P2'], 'NODE3'), [
			'Sim_NODE3_Nic_Slot3_P2',
			'Sim_Cable_SW3_Port25',
			'Sim_Cable_NODE3_Nic_Slot3_P2',
			'Sim_NODE3'
		]));
	it("the cable is the one the plant has: a port's menu offers pull", () => {
		const f = faultsFor(quiet(), simTagsFor('NODE3_Nic_Slot3_P2', ['SW3_Port25', 'NODE3_Nic_Slot3_P2'], 'NODE3'));
		assert.deepEqual(
			f.map((x) => `${x.tag}.${x.member}`),
			['Sim_NODE3_Nic_Slot3_P2.Down', 'Sim_Cable_SW3_Port25.Pulled']
		);
	});
	it('the device itself: only its own', () => assert.deepEqual(simTagsFor(undefined, [], 'SW3'), ['Sim_SW3']));
	it('a device picked as a part is not listed twice', () => assert.deepEqual(simTagsFor('SW3', [], 'SW3'), ['Sim_SW3']));
	it('an unbound part (no tag) offers its device', () => assert.deepEqual(simTagsFor(undefined, [undefined], 'NODE1'), ['Sim_NODE1']));
});

describe('activeFaults: what is set now', () => {
	it('none on a quiet plant', () => assert.deepEqual(activeFaults(quiet()), []));
	it('each member that differs from zero / false, by whose', () => {
		const s = {
			...quiet(),
			Sim_NODE1_Fan3: { Fail: true },
			Sim_Room: { InletDeltaC: 8 },
			Sim_Cable_SW3_Port25: { Pulled: true },
			Sim_SW3_Port25: { AdminDown: false, Down: false, ErrorRate: 0, SpeedMbps: 1000 },
			Scenario: 'fan-fail'
		};
		assert.deepEqual(
			activeFaults(s).map((f) => [f.who, f.label, f.value]),
			[
				['cable SW3 Port25', 'pull', true],
				['NODE1 Fan3', 'fail', true],
				['room', 'hot room +10 °C', 8],
				['SW3 Port25', '1G', 1000]
			]
		);
	});
	it('each can be cleared on its own: tag, member, clear value', () => {
		const [f] = activeFaults({ Sim_Net: { BroadcastPps: 1e6, MulticastPps: 0 } });
		assert.deepEqual([f.tag, f.member, f.clear], ['Sim_Net', 'BroadcastPps', 0]);
	});
	it('ignores what is not a fault struct', () => assert.deepEqual(activeFaults({ Sim_Weird: 3, Other: { Fail: true } }), []));
});

describe('the catalog and the clock', () => {
	it('parses ScenarioCatalog', () =>
		assert.deepEqual(parseCatalog('[{"name":"normal","about":"cleared"},{"name":"fan-fail"},{"about":"no name"}]'), [
			{ name: 'normal', about: 'cleared' },
			{ name: 'fan-fail', about: '' }
		]));
	it('anything else is none', () => {
		assert.deepEqual(parseCatalog('not json'), []);
		assert.deepEqual(parseCatalog(undefined), []);
		assert.deepEqual(parseCatalog('{"name":"x"}'), []);
	});
	it('ScenarioS reads as a duration', () => {
		assert.equal(since(42.7), '42 s');
		assert.equal(since(187), '3 m 07 s');
		assert.equal(since(3725), '1 h 02 m');
		assert.equal(since(undefined), '');
	});
	it('names read as a person would say them', () => {
		assert.equal(who('Sim_NODE1_Drive_NVMe2'), 'NODE1 Drive NVMe2');
		assert.equal(who('Sim_Net'), 'network');
	});
});
