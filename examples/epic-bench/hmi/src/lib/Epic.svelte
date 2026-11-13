<script lang="ts" module>
	// The bench groov EPIC, drawn from boxes in millimetres: a GRV-EPIC-PR1
	// with its touchscreen closed on a four-slot chassis, and the four
	// modules in slot order. Proportions are read off Opto 22's product
	// photos, not a CAD file.
	import type { Box3 } from './fit';

	/** World units per millimetre. */
	const S = 0.01;
	/** The board everything is mounted on, mm, before centring. */
	const BOARD = { x0: -22, x1: 218, y0: -100, y1: 94 };
	const CX = (BOARD.x0 + BOARD.x1) / 2;
	const CY = (BOARD.y0 + BOARD.y1) / 2;

	const box = (x0: number, x1: number, y0: number, y1: number, z0: number, z1: number): Box3 => ({
		min: [(x0 - CX) * S, (y0 - CY) * S, z0 * S],
		max: [(x1 - CX) * S, (y1 - CY) * S, z1 * S]
	});
	/** What the camera must keep in frame, world units: the board, and the controller standing off it. */
	export const BOUNDS: Box3[] = [box(BOARD.x0, BOARD.x1, BOARD.y0, BOARD.y1, -15, -11), box(0, 191, -82, 70, 0, 86)];

	export interface Channel {
		tag: string;
		/** What the tooltip says the value is, read when it is shown so it follows the tag. */
		text: () => string;
	}

	interface Module {
		slot: number;
		part: string;
		channels: number;
		kind: 'di' | 'do' | 'ai';
		prefix: string;
		unit?: string;
		digits?: number;
	}

	const MODULES: Module[] = [
		{ slot: 0, part: 'GRV-IACDCTTL-24', channels: 24, kind: 'di', prefix: 'EPIC_M0_DI' },
		{ slot: 1, part: 'GRV-ODCI-12', channels: 12, kind: 'do', prefix: 'EPIC_M1_DO' },
		{ slot: 2, part: 'GRV-IICTD-12', channels: 12, kind: 'ai', prefix: 'EPIC_M2_AI', unit: '°C', digits: 1 },
		{ slot: 3, part: 'GRV-IV-24', channels: 24, kind: 'ai', prefix: 'EPIC_M3_AI', unit: 'V', digits: 2 }
	];

	const tagOf = (m: Module, ch: number) => `${m.prefix}${String(ch).padStart(2, '0')}`;

	/** "1–11" for a run, "1, 3–5" for several. */
	function ranges(chs: number[]): string {
		const out: string[] = [];
		for (let i = 0; i < chs.length; i++) {
			let j = i;
			while (j + 1 < chs.length && chs[j + 1] === chs[j] + 1) j++;
			out.push(i === j ? `${chs[i]}` : `${chs[i]}–${chs[j]}`);
			i = j;
		}
		return out.join(', ');
	}
</script>

<script lang="ts">
	import { T } from '@threlte/core';
	import { RoundedBoxGeometry } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { AdditiveBlending, CanvasTexture, ExtrudeGeometry, Path, RepeatWrapping, Shape, SRGBColorSpace } from 'three';
	import { SCENE, type SceneContext } from '@joyautomation/nautilus-hmi-3d';
	import Led from './Led.svelte';
	import Print from './Print.svelte';
	import type { Ink, Paint } from './print';

	let {
		hovered = null,
		onhover
	}: {
		/** The channel under the pointer, to mark its row. */
		hovered?: string | null;
		onhover?: (ch: Channel | null) => void;
	} = $props();

	// The frame's tags and their quality come from the scene this model is
	// placed in (SceneView provides them), so the model is handed no client.
	const scene = getContext<SceneContext>(SCENE);
	const raw = (tag: string) => scene.tags[tag];
	const on = (tag: string) => raw(tag) === true;
	/** A number only when the controller has one and vouches for it. */
	const num = (tag: string): number | null => {
		const v = raw(tag);
		return typeof v === 'number' && Number.isFinite(v) && scene.isGood(tag) ? v : null;
	};
	let online = $derived(raw('EPIC__Online') === true);

	// Layout, mm. x runs along the rail, y up, z out of the panel.
	const SLOT0 = 94;
	const PITCH = 23.4;
	const MOD_W = 22;
	const MOD_D = 70;
	const COVER_W = 18;
	const COVER_H = 110;
	const COVER_TOP = 38;
	const COVER_Z = 77;
	const ROW0 = 10;
	const ROWS = 66;
	const cx = (m: Module) => SLOT0 + m.slot * PITCH + MOD_W / 2;
	const pitch = (m: Module) => ROWS / (m.channels - 1);
	const rowY = (m: Module, ch: number) => COVER_TOP - ROW0 - ch * pitch(m);

	const BLACK = '#141517';
	const COVER = '#1e2023';
	const STEEL = '#8b9096';

	function describe(m: Module, ch: number): Channel {
		const tag = tagOf(m, ch);
		if (m.kind !== 'ai') return { tag, text: () => (on(tag) ? 'on' : 'off') };
		return {
			tag,
			text: () => {
				const v = num(tag);
				return v === null ? 'bad quality (unwired)' : `${v.toFixed(m.digits)} ${m.unit}`;
			}
		};
	}

	/** The cover's lettering: the channel numbers, and the part number up its edge. */
	function coverInk(m: Module): Ink[] {
		const size = m.channels > 12 ? 2.2 : 2.9;
		const items: Ink[] = [];
		for (let ch = 0; ch < m.channels; ch++)
			items.push({ text: String(ch), x: 10.6, y: ROW0 + ch * pitch(m), size, align: 'right', color: '#eef0f2', weight: 600 });
		items.push({ text: m.part, x: 3.3, y: ROW0 + ROWS / 2, size: 2.5, align: 'center', rotate: -90, color: '#aab0b7', weight: 600, spacing: 0.15 });
		return items;
	}

	/** What an analog module's tag says: channel 0, then the rest in a line. */
	function calloutInk(m: Module): Ink[] {
		const v0 = num(tagOf(m, 0));
		const rest = Array.from({ length: m.channels - 1 }, (_, i) => i + 1);
		const bad = rest.filter((ch) => num(tagOf(m, ch)) === null);
		const vals = rest.map((ch) => num(tagOf(m, ch))).filter((v): v is number => v !== null);
		let line = '';
		if (bad.length) line = `ch ${ranges(bad)} unwired`;
		else if (vals.length) {
			const lo = Math.min(...vals).toFixed(m.digits);
			const hi = Math.max(...vals).toFixed(m.digits);
			const zero = (0).toFixed(m.digits);
			line = `ch ${ranges(rest)}  ${Number(lo) === 0 && Number(hi) === 0 ? zero : `${lo} to ${hi}`} ${m.unit}`;
		}
		return [
			{ text: `${m.part}  ch 0`, x: 3, y: 4.1, size: 2.2, color: '#a9a7a0', weight: 500 },
			v0 === null
				? { text: 'unwired', x: 3, y: 10.6, size: 4.6, color: '#fab219', weight: 650 }
				: { text: `${v0.toFixed(m.digits)} ${m.unit}`, x: 3, y: 10.6, size: 5, color: '#ffffff', weight: 650 },
			{ text: line, x: 3, y: 16.6, size: 2.2, color: bad.length ? '#fab219' : '#a9a7a0', weight: 500 }
		];
	}
	const CALLOUT = { w: 54, h: 20, y: 78 };
	// Each tag sits over its own module's top, shifted so the two don't meet.
	const calloutX = (m: Module) => (m.slot === 2 ? cx(m) - CALLOUT.w / 2 + 3 : cx(m) + CALLOUT.w / 2 - 17);

	// The chassis's slotted steel floor: one drawn tile, repeated along it.
	const vents = (() => {
		const c = document.createElement('canvas');
		c.width = 117;
		c.height = 230;
		const ctx = c.getContext('2d')!;
		ctx.fillStyle = STEEL;
		ctx.fillRect(0, 0, c.width, c.height);
		ctx.fillStyle = '#0d0e10';
		for (let row = 0; row < 9; row++) {
			ctx.beginPath();
			ctx.roundRect(18, 26 + row * 21, 81, 9, 3);
			ctx.fill();
		}
		const t = new CanvasTexture(c);
		t.colorSpace = SRGBColorSpace;
		t.wrapS = RepeatWrapping;
		t.repeat.set(8, 1);
		t.anisotropy = 8;
		return t;
	})();

	// The touchscreen's housing, seen from the front: a frame round a sunken
	// display, whose lower edge sweeps from high on the left down to a
	// rounded tongue on the right, where the wordmark sits.
	const SCREEN = { w: 92, top: 70, z: 68.5, d: 8 };
	const DISPLAY = { x0: 13, x1: 79, y0: -42, y1: 62 };
	const bezel = (() => {
		const o = new Shape();
		o.moveTo(4, SCREEN.top);
		o.lineTo(SCREEN.w - 4, SCREEN.top);
		o.quadraticCurveTo(SCREEN.w, SCREEN.top, SCREEN.w, SCREEN.top - 4);
		o.lineTo(SCREEN.w, -66);
		o.quadraticCurveTo(SCREEN.w, -79, SCREEN.w - 13, -79);
		o.lineTo(58, -79);
		o.bezierCurveTo(34, -79, 22, -66, 9, -57);
		o.bezierCurveTo(3, -53, 0, -52, 0, -46);
		o.lineTo(0, SCREEN.top - 4);
		o.quadraticCurveTo(0, SCREEN.top, 4, SCREEN.top);
		const hole = new Path();
		hole.moveTo(DISPLAY.x0, DISPLAY.y0);
		hole.lineTo(DISPLAY.x1, DISPLAY.y0);
		hole.lineTo(DISPLAY.x1, DISPLAY.y1);
		hole.lineTo(DISPLAY.x0, DISPLAY.y1);
		hole.closePath();
		o.holes.push(hole);
		// the bevel slopes the frame's inner walls down toward the display
		return new ExtrudeGeometry(o, { depth: SCREEN.d, bevelEnabled: true, bevelSize: 1.6, bevelThickness: 1.6, bevelSegments: 3, curveSegments: 24 });
	})();

	// A module's hinged cover: its top edge is a wave, low on the left and
	// high on the right, and the status light is the band that follows it in
	// the channel between the cover and the module's head.
	const WAVE = { x: 9, lo: 32.5, hi: 46 };
	const cover = (() => {
		const o = new Shape();
		o.moveTo(-WAVE.x, -63);
		o.lineTo(-WAVE.x, WAVE.lo);
		o.bezierCurveTo(-2, WAVE.lo + 0.5, 2, WAVE.lo + 6, WAVE.x, WAVE.hi);
		o.lineTo(WAVE.x, -63);
		o.absarc(0, -63, WAVE.x, 0, -Math.PI, true);
		return new ExtrudeGeometry(o, { depth: 5.6, bevelEnabled: true, bevelSize: 0.7, bevelThickness: 0.7, bevelSegments: 3, curveSegments: 24 });
	})();
	const lens = (() => {
		const o = new Shape();
		o.moveTo(-WAVE.x, WAVE.lo + 1.4);
		o.bezierCurveTo(-2, WAVE.lo + 1.9, 2, WAVE.lo + 7.4, WAVE.x, WAVE.hi + 1.4);
		o.lineTo(WAVE.x, WAVE.hi + 2.6);
		o.bezierCurveTo(2, WAVE.lo + 9.4, -2, WAVE.lo + 4.6, -WAVE.x, WAVE.lo + 4);
		o.closePath();
		return new ExtrudeGeometry(o, { depth: 0.8, bevelEnabled: false, curveSegments: 24 });
	})();
	/** The soft light the lens throws on the head above it. */
	const haze = (() => {
		const c = document.createElement('canvas');
		c.width = c.height = 64;
		const ctx = c.getContext('2d')!;
		const g = ctx.createRadialGradient(32, 32, 0, 32, 32, 32);
		g.addColorStop(0, 'rgba(255,255,255,0.55)');
		g.addColorStop(1, 'rgba(255,255,255,0)');
		ctx.fillStyle = g;
		ctx.fillRect(0, 0, 64, 64);
		return new CanvasTexture(c);
	})();
	/** The mark printed on a module's head: two linked rings, gloss black on the matte face. */
	const rings: Paint = (ctx) => {
		ctx.strokeStyle = '#08080a';
		ctx.lineWidth = 1.5;
		for (const x of [4.6, 11.4]) {
			ctx.beginPath();
			ctx.arc(x, 4.5, 2.9, 0, Math.PI * 2);
			ctx.stroke();
		}
	};
</script>

<T.Group scale={S} position={[-CX * S, -CY * S, 0]}>
	<!-- the board, the DIN rail and the chassis's steel back -->
	<T.Mesh position={[CX, CY, -13]}>
		<RoundedBoxGeometry args={[BOARD.x1 - BOARD.x0, BOARD.y1 - BOARD.y0, 3]} radius={1.4} />
		<T.MeshStandardMaterial color="#3b4047" roughness={0.9} metalness={0} />
	</T.Mesh>
	<T.Mesh position={[98, 0, -8]}>
		<T.BoxGeometry args={[226, 35, 7]} />
		<T.MeshStandardMaterial color="#9a9ea4" roughness={0.45} metalness={0.7} />
	</T.Mesh>
	<T.Mesh position={[98, 0, -4.4]}>
		<T.BoxGeometry args={[226, 25, 0.4]} />
		<T.MeshStandardMaterial color="#8f9398" roughness={0.5} metalness={0.9} />
	</T.Mesh>
	<T.Mesh position={[95, -6.6, -2.2]}>
		<T.BoxGeometry args={[196, 150, 2.4]} />
		<T.MeshStandardMaterial color={STEEL} roughness={0.5} metalness={0.6} />
	</T.Mesh>
	<!-- the chassis floor: a flat slotted tray under the processor and the modules -->
	<T.Mesh position={[95, -80.8, 43]} rotation={[-Math.PI / 2, 0, 0]}>
		<T.BoxGeometry args={[196, 86, 1.6]} />
		<T.MeshStandardMaterial map={vents} roughness={0.45} metalness={0.6} />
	</T.Mesh>

	<!-- GRV-EPIC-PR1: the processor's body, and its touchscreen closed over it -->
	<T.Mesh position={[46, -4, 34]}>
		<T.BoxGeometry args={[84, 138, 68]} />
		<T.MeshStandardMaterial color={BLACK} roughness={0.6} metalness={0.15} />
	</T.Mesh>
	<T.Mesh geometry={bezel} position={[0, 0, SCREEN.z]}>
		<T.MeshStandardMaterial color={COVER} roughness={0.5} metalness={0.2} />
	</T.Mesh>
	<!-- the display, dark, sunk behind the frame -->
	<T.Mesh position={[(DISPLAY.x0 + DISPLAY.x1) / 2, (DISPLAY.y0 + DISPLAY.y1) / 2, SCREEN.z + 3]}>
		<T.BoxGeometry args={[DISPLAY.x1 - DISPLAY.x0 + 4, DISPLAY.y1 - DISPLAY.y0 + 4, 0.6]} />
		<T.MeshStandardMaterial color="#07080a" roughness={0.12} metalness={0.6} />
	</T.Mesh>
	<Print
		w={60}
		h={22}
		position={[60, -62.5, SCREEN.z + SCREEN.d + 1.7]}
		items={[
			{ text: 'groov', x: 30, y: 7.6, size: 9, align: 'center', italic: true, weight: 800, color: '#c5c9ce' },
			{ text: 'GRV-EPIC-PR1', x: 30, y: 17.5, size: 2.6, align: 'center', weight: 600, color: '#aab0b7', spacing: 0.5 }
		]}
	/>

	{#each MODULES as m (m.slot)}
		<!-- the module's body, its status lens, and the hinged cover over the terminals -->
		<T.Mesh position={[cx(m), -3, MOD_D / 2]}>
			<RoundedBoxGeometry args={[MOD_W, 128, MOD_D]} radius={2.2} />
			<T.MeshStandardMaterial color={BLACK} roughness={0.6} metalness={0.15} />
		</T.Mesh>
		<!-- the head: the face the cover's wave is cut against, with the two rings printed on it -->
		<T.Mesh position={[cx(m), 43, MOD_D + 1.6]}>
			<RoundedBoxGeometry args={[MOD_W - 1, 36, 6.4]} radius={2.4} />
			<T.MeshStandardMaterial color={COVER} roughness={0.55} metalness={0.15} />
		</T.Mesh>
		<Print w={16} h={9} position={[cx(m), 54.5, MOD_D + 4.9]} items={[]} paint={rings} res={14} />
		<T.Mesh geometry={lens} position={[cx(m), 0, MOD_D + 4.85]}>
			<T.MeshBasicMaterial color={online ? '#7fe9f2' : '#16242a'} toneMapped={false} />
		</T.Mesh>
		{#if online}
			<T.Mesh position={[cx(m) - 2, 42, MOD_D + 4.95]} renderOrder={3}>
				<T.PlaneGeometry args={[22, 20]} />
				<T.MeshBasicMaterial map={haze} color="#35c6d8" transparent opacity={0.5} blending={AdditiveBlending} depthWrite={false} toneMapped={false} />
			</T.Mesh>
		{/if}
		<T.Mesh geometry={cover} position={[cx(m), 0, COVER_Z - 6.3]}>
			<T.MeshStandardMaterial color={COVER} roughness={0.45} metalness={0.2} />
		</T.Mesh>
		<Print w={COVER_W} h={COVER_H} position={[cx(m), COVER_TOP - COVER_H / 2, COVER_Z + 0.08]} items={coverInk(m)} res={12} />

		{#each { length: m.channels } as _, ch (ch)}
			{@const tag = tagOf(m, ch)}
			{@const y = rowY(m, ch)}
			{@const h = Math.min(2.3, pitch(m) * 0.6)}
			{#if m.kind === 'ai'}
				{@const v = num(tag)}
				<!-- an analog channel has no lamp on the real module: this is the HMI's
				     mark, lit with a reading, amber and small where the controller
				     reports the channel bad (nothing wired to it) -->
				<Led
					position={[cx(m) + 5, y, COVER_Z + 0.3]}
					size={v === null ? [1.6, h * 0.7] : [3.8, h]}
					on={v === null || Math.abs(v) >= 0.05}
					color={v === null ? '#fab219' : '#7fd8ff'}
					spread={v === null ? 1.6 : 3}
				/>
			{:else}
				<Led position={[cx(m) + 5, y, COVER_Z + 0.3]} size={[3.8, h]} on={on(tag)} />
			{/if}
			{#if hovered === tag}
				<T.Mesh position={[cx(m), y, COVER_Z + 0.15]}>
					<T.PlaneGeometry args={[COVER_W - 1, Math.max(pitch(m), 2.6)]} />
					<T.MeshBasicMaterial color="#ffffff" transparent opacity={0.16} depthWrite={false} toneMapped={false} />
				</T.Mesh>
			{/if}
			<!-- the whole row is the pointer's target, not the 2 mm lamp -->
			<T.Mesh
				position={[cx(m), y, COVER_Z + 0.6]}
				onpointerenter={() => onhover?.(describe(m, ch))}
				onpointerleave={() => onhover?.(null)}
			>
				<T.PlaneGeometry args={[COVER_W, pitch(m)]} />
				<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
			</T.Mesh>
		{/each}

		{#if m.kind === 'ai'}
			<T.Mesh position={[cx(m), (61 + CALLOUT.y - CALLOUT.h / 2) / 2, MOD_D - 4]}>
				<T.BoxGeometry args={[0.5, CALLOUT.y - CALLOUT.h / 2 - 61, 0.5]} />
				<T.MeshBasicMaterial color="#d8d6d0" toneMapped={false} />
			</T.Mesh>
			<Print w={CALLOUT.w} h={CALLOUT.h} position={[calloutX(m), CALLOUT.y, MOD_D - 4]} items={calloutInk(m)} plate={{ fill: 'rgba(22,22,21,0.94)', stroke: '#55554f', radius: 2 }} res={14} />
		{/if}
	{/each}
</T.Group>
