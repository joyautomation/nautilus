<script lang="ts">
	// PLC runtime diagnostics panel: stat tiles, scan-time and period
	// sparklines, scan-time distribution, and the last scan's phase breakdown
	// (input read → logic execute → output write). Feed it `frame.scan` from
	// the nautilus stream and it renders the whole story of the scan loop.
	import Sparkline from './Sparkline.svelte';
	import Histogram from './Histogram.svelte';
	import StatusPill from './StatusPill.svelte';
	import type { ScanStats } from '../types.js';

	let { scan }: { scan: ScanStats } = $props();

	const fmt = (v: number, digits = 1) => (isFinite(v) ? v.toFixed(digits) : '—');

	// Phase widths as fractions of the last scan (logic is µs — usually a sliver).
	let phases = $derived.by(() => {
		const exec = scan.execUs / 1000;
		const total = scan.readMs + exec + scan.writeMs || 1;
		return {
			read: (scan.readMs / total) * 100,
			exec: Math.max((exec / total) * 100, 0.5),
			write: (scan.writeMs / total) * 100
		};
	});
</script>

<div class="diag">
	<div class="tiles">
		<div class="tile">
			<span class="label">scan target</span>
			<span class="value">{fmt(scan.targetMs, 0)}<small>ms</small></span>
		</div>
		<div class="tile">
			<span class="label">last scan</span>
			<span class="value">{fmt(scan.lastMs)}<small>ms</small></span>
		</div>
		<div class="tile">
			<span class="label">average</span>
			<span class="value">{fmt(scan.avgMs)}<small>ms</small></span>
		</div>
		<div class="tile">
			<span class="label">min / max</span>
			<span class="value">{fmt(scan.minMs)} / {fmt(scan.maxMs)}<small>ms</small></span>
		</div>
		<div class="tile">
			<span class="label">period jitter</span>
			<span class="value">{fmt(scan.jitterMs, 2)}<small>ms</small></span>
		</div>
		<div class="tile">
			<span class="label">scan count</span>
			<span class="value">{scan.count.toLocaleString()}</span>
		</div>
	</div>

	<div class="charts">
		<div class="chart">
			<span class="label">scan time — last {scan.recent.length} scans</span>
			<Sparkline values={scan.recent} yMin={0} height={56} />
		</div>
		<div class="chart">
			<span class="label">scan period — target {fmt(scan.targetMs, 0)} ms</span>
			<Sparkline values={scan.periods} color="var(--s2)" height={56} />
		</div>
		<div class="chart">
			<span class="label">scan time distribution</span>
			<Histogram counts={scan.histogram} />
		</div>
	</div>

	<div class="phase">
		<span class="label">last scan phase breakdown</span>
		<div class="bar" role="img" aria-label="phase breakdown: input read, logic execute, output write">
			<span class="read" style="width: {phases.read}%"></span>
			<span class="exec" style="width: {phases.exec}%"></span>
			<span class="write" style="width: {phases.write}%"></span>
		</div>
		<div class="legend">
			<span><i class="read"></i>input read <b>{fmt(scan.readMs, 2)} ms</b></span>
			<span><i class="exec"></i>logic execute <b>{fmt(scan.execUs, 0)} µs</b></span>
			<span><i class="write"></i>output write <b>{fmt(scan.writeMs, 2)} ms</b></span>
			<span class="pillbox">
				<StatusPill
					kind={scan.ioHealthy ? 'good' : 'critical'}
					label={scan.ioHealthy
						? `I/O healthy · ${scan.ioErrors} errors`
						: (scan.lastIOError ?? 'I/O fault')}
				/>
			</span>
		</div>
	</div>
</div>

<style>
	.diag {
		display: grid;
		gap: 14px;
	}
	.label {
		display: block;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--muted);
		margin-bottom: 4px;
	}
	.tiles {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
		gap: 10px;
	}
	.tile {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 10px 12px;
	}
	.value {
		font-size: 20px;
		font-weight: 650;
		font-family: var(--mono);
		color: var(--ink);
	}
	.value small {
		font-size: 11px;
		color: var(--muted);
		margin-left: 2px;
	}
	.charts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 10px;
	}
	.chart {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 10px 12px;
	}
	.phase {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 10px 12px;
	}
	.bar {
		display: flex;
		height: 14px;
		border-radius: 7px;
		overflow: hidden;
		background: var(--surface-2);
	}
	.bar .read { background: var(--s1); }
	.bar .exec { background: var(--warn); }
	.bar .write { background: var(--s2); }
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 14px;
		align-items: center;
		margin-top: 8px;
		font-size: 12px;
		color: var(--ink-2);
	}
	.legend i {
		display: inline-block;
		width: 10px;
		height: 10px;
		border-radius: 3px;
		margin-right: 5px;
		vertical-align: -1px;
	}
	.legend i.read { background: var(--s1); }
	.legend i.exec { background: var(--warn); }
	.legend i.write { background: var(--s2); }
	.legend b {
		font-family: var(--mono);
		font-weight: 600;
	}
	.pillbox {
		margin-left: auto;
	}
</style>
