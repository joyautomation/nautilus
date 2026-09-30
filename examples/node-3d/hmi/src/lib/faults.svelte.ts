// The plant's fault inputs and scenario tags, polled from /plant/api (the
// vite proxy to the plant when PLANT_URL is set). One per page, shared by
// the scenario panel and the right-click menu. `up` is false until the
// plant answers — a live controller has no plant, and nothing that writes
// faults shows.
import { parseCatalog, type Scenario } from './faults';

const POLL_MS = 2000;

export class PlantFaults {
	up = $state(false);
	/** Every `Sim_*` struct, by tag. */
	state = $state<Record<string, unknown>>({});
	catalog = $state<Scenario[]>([]);
	scenario = $state<string>('');
	/** Seconds since `Scenario` was written. */
	since = $state<number | undefined>(undefined);
	unknown = $state(false);
	#timer: ReturnType<typeof setInterval> | undefined;

	async poll() {
		try {
			const r = await fetch('/plant/api/state?tags=Sim_*,Scenario*');
			if (!r.ok) throw new Error(r.statusText);
			const t: Record<string, unknown> = (await r.json()).tags ?? {};
			if (typeof t.ScenarioCatalog !== 'string') throw new Error('not the plant');
			const sims: Record<string, unknown> = {};
			for (const [k, v] of Object.entries(t)) if (k.startsWith('Sim_')) sims[k] = v;
			this.state = sims;
			// The catalog is written once; parse it only when it changes.
			if (!this.catalog.length) this.catalog = parseCatalog(t.ScenarioCatalog);
			this.scenario = typeof t.Scenario === 'string' ? t.Scenario : '';
			this.since = typeof t.ScenarioS === 'number' ? t.ScenarioS : undefined;
			this.unknown = t.ScenarioUnknown === true;
			this.up = true;
		} catch {
			this.up = false;
		}
	}
	start() {
		this.poll();
		this.#timer = setInterval(() => this.poll(), POLL_MS);
	}
	stop() {
		clearInterval(this.#timer);
	}
	async write(name: string, value: boolean | number | string) {
		await fetch('/plant/api/tags', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ name, value })
		}).catch(() => {});
		await this.poll();
	}
	run = (scenario: string) => this.write('Scenario', scenario);
	// The plant acts on a CHANGE of Scenario: once it reads normal, faults
	// set by hand since are cleared with the ClearFaults pulse instead.
	clearAll = () => (this.scenario === 'normal' ? this.write('ClearFaults', true) : this.run('normal'));
}
