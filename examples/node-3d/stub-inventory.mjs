// Stub the inventory tags workstream A will publish (Drive, PCIeDevice,
// DIMM, CPU — docs/design/spatial-hmi.md §3e) from a Redfish capture of the
// node, so the 3D view has node1's real parts before the driver does.
//
//   node stub-inventory.mjs <capture dir> [NODE1] > hmi/src/lib/node1.stub.json
//
// Serial numbers are redacted: the box is a customer's. Values are the
// UDT members agreed in the chassis profile's `types`; `live` names the
// bench tags a member follows at run time (temperatures the recorded BMC
// already publishes as TempSensor tags).
import { readFileSync, existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const [dir, node = 'NODE1'] = process.argv.slice(2);
if (!dir) {
	console.error('usage: node stub-inventory.mjs <redfish capture dir> [NODE]');
	process.exit(2);
}
const read = (p) => JSON.parse(readFileSync(join(dir, p, 'index.json'), 'utf8'));
const members = (p) => read(p).Members.map((m) => m['@odata.id'].replace(/^\/redfish\/v1\//, ''));
const HEALTH = { OK: 0, Warning: 1, Critical: 2 };
const health = (s) => HEALTH[s?.Health] ?? 3;
const present = (s) => s?.State !== 'Absent';
const sensor = (id) => {
	const p = join('Chassis/1/Sensors', id);
	return existsSync(join(dir, p, 'index.json')) ? read(p).Reading : undefined;
};
const tags = {};
const live = {};

// Drives: the NVMe backplane and the RAID enclosure's boot pair.
for (const coll of readdirSync(join(dir, 'Chassis')).filter((c) => existsSync(join(dir, 'Chassis', c, 'Drives')))) {
	for (const d of members(`Chassis/${coll}/Drives`).map(read)) {
		const bay = d.PhysicalLocation?.PartLocation?.LocationOrdinalValue ?? Number(d.Id.split('.').pop());
		const nvme = d.Protocol === 'NVMe';
		const name = nvme ? `${node}_Drive_NVMe${bay}` : `${node}_Drive_Boot${bay}`;
		tags[name] = {
			Bay: bay,
			Name: d.Name,
			Model: d.Model,
			Serial: '(redacted)',
			CapacityGB: Math.round(d.CapacityBytes / 1e9),
			Protocol: d.Protocol,
			MediaType: d.MediaType ?? 'SSD',
			Health: health(d.Status),
			Fault: health(d.Status) === 2,
			PredictedFailure: d.FailurePredicted === true,
			TempC: d.Oem?.Supermicro?.Temperature ?? null,
			Present: present(d.Status)
		};
	}
}

// Add-in cards: the slot is in the device's name ("NIC1 System Slot2 …").
for (const p of members('Chassis/1/PCIeDevices').map(read)) {
	const slot = Number(/System Slot(\d+)/.exec(p.Name)?.[1]);
	if (!slot) continue; // the NVMe drives list here too, without a slot
	const fns = read(p.PCIeFunctions['@odata.id'].replace(/^\/redfish\/v1\//, ''))['Members@odata.count'];
	tags[`${node}_Pcie_Slot${slot}`] = {
		Slot: slot,
		Name: p.Name,
		Model: p.Model,
		Manufacturer: p.Manufacturer,
		Firmware: p.FirmwareVersion,
		Health: health(p.Status),
		Fault: health(p.Status) === 2,
		Ports: fns,
		TempC: sensor(`AOC_NIC${slot}Temp`) ?? null
	};
}

// DIMMs, by locator (DIMMA1 → Dimm_A1); temperature per bank of four.
for (const m of members('Systems/1/Memory').map(read)) {
	if (m.Status?.State === 'Absent') continue;
	const loc = m.DeviceLocator.replace(/^DIMM/, '');
	const name = `${node}_Dimm_${loc}`;
	tags[name] = {
		Locator: m.DeviceLocator,
		CapacityGB: m.CapacityMiB / 1024,
		Manufacturer: m.Manufacturer,
		PartNumber: m.PartNumber,
		Health: health(m.Status),
		Fault: health(m.Status) === 2,
		TempC: sensor('ABCD'.includes(loc[0]) ? 'DIMMA~DTemp' : 'DIMME~HTemp') ?? null
	};
	live[`${name}.TempC`] = `${node}_Temp_DIMM.Value`;
}

for (const c of members('Systems/1/Processors').map(read)) {
	const i = c.Id;
	tags[`${node}_Cpu${i}`] = {
		Model: c.Model.replace(/\(R\)/g, '').replace(/^Intel Xeon/, 'Xeon').replace(/^Intel /, ''),
		Cores: c.TotalCores,
		Health: health(c.Status),
		Fault: health(c.Status) === 2,
		TempC: sensor('CPUTemp') ?? null,
		Pct: 0
	};
	live[`${node}_Cpu${i}.TempC`] = `${node}_Temp_CPU.Value`;
	live[`${node}_Cpu${i}.Pct`] = `${node}.CpuPct`;
}

const captured = /redfish-(\d{4})(\d{2})(\d{2})/.exec(dir);
process.stdout.write(
	JSON.stringify(
		{
			source: `BMC capture${captured ? ` ${captured[1]}-${captured[2]}-${captured[3]}` : ''}, stubbed until the drivers publish these tags (workstream A); serials redacted`,
			tags,
			live
		},
		null,
		'\t'
	) + '\n'
);
