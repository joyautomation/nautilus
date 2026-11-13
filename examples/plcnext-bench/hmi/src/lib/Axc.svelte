<script lang="ts" module>
	// The bench PLCnext station, drawn from boxes in millimetres: an
	// AXC F 2152 and, on its right, an AXL F BP SE4 backplane holding four
	// Axioline Smart Elements, two over two. Sizes are the datasheets'
	// (45 x 126.9 mm controller, 40 x 122.4 mm backplane, 15 x 62 mm
	// elements); the Smart Elements' faces follow Phoenix Contact's product
	// photos, the controller's face is from memory of the part.
	import type { Box3 } from './fit';

	/** World units per millimetre. */
	const S = 0.01;
	/** The board everything is mounted on, mm, before centring. */
	const BOARD = { x0: -22, x1: 107, y0: -96, y1: 76 };
	const CX = (BOARD.x0 + BOARD.x1) / 2;
	const CY = (BOARD.y0 + BOARD.y1) / 2;
	const box = (x0: number, x1: number, y0: number, y1: number, z0: number, z1: number): Box3 => ({
		min: [(x0 - CX) * S, (y0 - CY) * S, z0 * S],
		max: [(x1 - CX) * S, (y1 - CY) * S, z1 * S]
	});
	/** What the camera must keep in frame, world units: the board, and the station standing off it. */
	export const BOUNDS: Box3[] = [box(BOARD.x0, BOARD.x1, BOARD.y0, BOARD.y1, -15, -11), box(0, 85, -64, 64, 0, 76), box(35, 89, -87, -69, 50, 50)];

	export interface Channel {
		tag: string;
		/** What the tooltip says the value is, read when it is shown so it follows the tag. */
		text: () => string;
	}

	/** An Axioline analog word in the error range, 0x8000..0x80FF, names a fault instead of a reading. */
	export function analogFault(v: unknown): string | null {
		if (typeof v !== 'number') return 'no value';
		const word = v & 0xffff;
		if (word < 0x8000 || word > 0x80ff) return null;
		return word === 0x8020 ? 'supply missing' : `fault 0x${word.toString(16).toUpperCase()}`;
	}

	interface Element {
		/** Position on the local bus, 1..4. */
		slot: number;
		part: string;
		/** The short name printed on the element. */
		mark: string;
		kind: 'do' | 'di' | 'cover' | 'ai';
		prefix?: string;
		channels: number;
	}

	const ELEMENTS: Element[] = [
		{ slot: 1, part: 'AXL SE DO16/1', mark: 'DO16/1', kind: 'do', prefix: 'DO_', channels: 16 },
		{ slot: 2, part: 'AXL SE DI16/1', mark: 'DI16/1', kind: 'di', prefix: 'DI_', channels: 16 },
		{ slot: 3, part: 'AXL SE SC-A', mark: 'SC-A', kind: 'cover', channels: 0 },
		{ slot: 4, part: 'AXL SE AI4 U', mark: 'AI4 U', kind: 'ai', prefix: 'AI_', channels: 4 }
	];

	/** LED n on a digital element is tag index n-1: LED 01 is DO_00. */
	const tagOf = (e: Element, ch: number) => `${e.prefix}${String(ch).padStart(2, '0')}`;
</script>

<script lang="ts">
	import { T } from '@threlte/core';
	import { RoundedBoxGeometry } from '@threlte/extras';
	import { getContext } from 'svelte';
	import { SCENE, type SceneContext } from '@joyautomation/nautilus-hmi-3d';
	import Led from './Led.svelte';
	import Print from './Print.svelte';
	import type { Ink, Paint } from './print';

	let {
		online = false,
		hovered = null,
		onhover
	}: {
		/** The driver is exchanging the process image with the local bus. */
		online?: boolean;
		/** The channel under the pointer, to mark its row. */
		hovered?: string | null;
		onhover?: (ch: Channel | null) => void;
	} = $props();

	// The frame's tags come from the scene this model is placed in
	// (SceneView provides them), so the model is handed no client.
	const scene = getContext<SceneContext>(SCENE);
	const raw = (tag: string) => scene.tags[tag];
	const on = (tag: string) => raw(tag) === true;
	let live = $derived(Object.keys(scene.tags).length > 0);

	// Layout, mm. x runs along the rail, y up, z out of the panel.
	const AXC = { w: 45, h: 126.9, d: 75 };
	const BP = { x: 45, w: 40, h: 122.4, d: 36.9 };
	const SE = { w: 14.6, h: 60, z0: 20, z1: 54 };
	/** Slots run down the left column, then down the right (the backplane's own 1 2 / 3 4 marking). */
	const seX = (e: Element) => BP.x + 10 + (e.slot > 2 ? 15 : 0) + 0.2;
	const seTop = (e: Element) => (e.slot % 2 === 1 ? 60.6 : -0.6);
	// On a digital element's face, from its top edge: 01..08, the latch, 09..16.
	const ROW = 2.75;
	const rowAt = (ch: number) => 8.6 + ch * ROW + (ch >= 8 ? 4.4 : 0);
	/** An AI4 U's channel n is its terminal pair n: two 3 mm terminals. */
	const aiAt = (ch: number) => 9.5 + ch * 6;

	const GREY = '#8f9496';
	const GREY_2 = '#7c8183';
	const INK = '#2a2d30';
	const ORANGE = '#ef7d00';
	const YELLOW = '#ffd21f';

	function describe(e: Element, ch: number): Channel {
		const tag = tagOf(e, ch);
		if (e.kind !== 'ai') return { tag, text: () => (on(tag) ? 'on' : 'off') };
		return { tag, text: () => analogFault(raw(tag)) ?? `${raw(tag)} counts` };
	}

	/** A column of push-in terminals: the white release beside the conductor's hole. */
	const terminals =
		(n: number): Paint =>
		(ctx) => {
			ctx.fillStyle = '#34383c';
			ctx.beginPath();
			ctx.roundRect(5.9, 6.4, 5.8, n * 3 + 0.4, 0.5);
			ctx.fill();
			for (let k = 0; k < n; k++) {
				const y = 6.6 + k * 3;
				ctx.fillStyle = '#e9ebec';
				ctx.fillRect(6.3, y + 0.4, 1.9, 2.2);
				ctx.fillStyle = '#0c0d0e';
				ctx.beginPath();
				ctx.arc(9.9, y + 1.5, 1.05, 0, Math.PI * 2);
				ctx.fill();
			}
		};
	/** Where a cover has light pipes and no lamps behind them. */
	const pipes: Paint = (ctx) => {
		ctx.fillStyle = '#868b8d';
		for (let ch = 0; ch < 16; ch++) ctx.fillRect(0.9, rowAt(ch) - 0.7, 1.4, 1.4);
	};

	function faceInk(e: Element): Ink[] {
		const items: Ink[] = [
			{ text: e.mark, x: 0.8, y: 2.3, size: 1.25, color: INK, weight: 600 },
			{ text: e.part, x: 13.3, y: 30, size: 1.35, align: 'center', rotate: -90, color: INK, weight: 600, spacing: 0.1 }
		];
		if (e.kind === 'cover') return items;
		items.push({ text: 'E', x: 3.1, y: 5.3, size: 1.25, color: INK, weight: 600 });
		if (e.kind === 'ai') return items;
		for (let ch = 0; ch < e.channels; ch++) items.push({ text: String(ch + 1).padStart(2, '0'), x: 2.9, y: rowAt(ch), size: 1.35, color: INK, weight: 600 });
		return items;
	}

	// The AI4 U's tag: what its four words say. One line when they agree.
	const ai = ELEMENTS[3];
	function calloutInk(): Ink[] {
		const vals = Array.from({ length: ai.channels }, (_, ch) => raw(tagOf(ai, ch)));
		const faults = vals.map(analogFault);
		const same = faults.every((f) => f !== null && f === faults[0]);
		const hex = (v: unknown) => (typeof v === 'number' ? `0x${(v & 0xffff).toString(16).toUpperCase().padStart(4, '0')}` : '—');
		const head: Ink = { text: `${ai.part}   AI_00 to AI_03`, x: 2.6, y: 3.7, size: 1.8, color: '#a9a7a0', weight: 500 };
		if (!live) return [head, { text: '—', x: 2.6, y: 9.4, size: 4, color: '#a9a7a0', weight: 650 }];
		if (same)
			return [
				head,
				{ text: faults[0]!, x: 2.6, y: 9.4, size: 4, color: '#fab219', weight: 650 },
				{ text: `raw ${hex(vals[0])} on all four inputs`, x: 2.6, y: 14.6, size: 1.8, color: '#fab219', weight: 500 }
			];
		return [
			head,
			{ text: vals.map((v, i) => (faults[i] ? '!' : String(v))).join('   '), x: 2.6, y: 9.4, size: 3.7, color: '#ffffff', weight: 650 },
			{ text: faults.some(Boolean) ? faults.map((f, i) => (f ? `AI_0${i} ${f}` : '')).filter(Boolean).join(', ') : 'raw counts', x: 2.6, y: 14.6, size: 1.8, color: faults.some(Boolean) ? '#fab219' : '#a9a7a0', weight: 500 }
		];
	}
	const CALLOUT = { w: 54, h: 17.5, x: 62, y: -78 };

	// The controller's own lamps, top right of its face. Only two are driven
	// here: UL (it is powered: it is serving this page) and D (the local bus
	// is running, from the driver's status); the rest are drawn dark.
	const LAMPS = [
		['UL', 'BF-C'],
		['RUN', 'BF-D'],
		['FAIL', 'SF'],
		['DBG', 'D'],
		['', 'E']
	];
	const axcInk: Ink[] = [
		...LAMPS.flatMap((pair, i) => pair.map((text, j): Ink => ({ text, x: 27.6 + j * 8.4, y: 10 + i * 4.6, size: 1.3, color: INK, weight: 600 }))),
		{ text: 'AXC F 2152', x: 22.5, y: 49, size: 2.7, align: 'center', color: INK, weight: 650, spacing: 0.2 },
		{ text: 'PLCnext', x: 22.5, y: 57.5, size: 3.6, align: 'center', color: '#5f8f00', weight: 800, italic: true },
		{ text: 'Technology', x: 22.5, y: 62.6, size: 1.5, align: 'center', color: '#5f8f00', weight: 600 },
		{ text: 'X1', x: 23.5, y: 80, size: 1.6, color: INK, weight: 600 },
		{ text: 'X2', x: 23.5, y: 100, size: 1.6, color: INK, weight: 600 },
		{ text: 'SD', x: 30, y: 113.5, size: 1.4, color: INK, weight: 600 }
	];
	const axcPaint: Paint = (ctx) => {
		// the lamp window, the SD slot and the reset hole
		ctx.fillStyle = '#7f8486';
		ctx.beginPath();
		ctx.roundRect(24.4, 6.6, 18, 24.4, 0.8);
		ctx.fill();
		ctx.fillStyle = '#1a1c1e';
		ctx.fillRect(30, 116, 11, 1.4);
		ctx.beginPath();
		ctx.arc(36, 104, 0.9, 0, Math.PI * 2);
		ctx.fill();
	};
	const plugPaint: Paint = (ctx) => {
		for (const [x, y] of [[3.5, 6], [10.5, 6], [3.5, 18], [10.5, 18]]) {
			ctx.fillStyle = '#e9ebec';
			ctx.fillRect(x - 1.6, y - 4.4, 3.2, 2.6);
			ctx.fillStyle = '#0c0d0e';
			ctx.beginPath();
			ctx.arc(x, y + 1.2, 1.7, 0, Math.PI * 2);
			ctx.fill();
		}
	};
</script>

<T.Group scale={S} position={[-CX * S, -CY * S, 0]}>
	<!-- the board and the DIN rail -->
	<T.Mesh position={[CX, CY, -13]}>
		<RoundedBoxGeometry args={[BOARD.x1 - BOARD.x0, BOARD.y1 - BOARD.y0, 3]} radius={1.4} />
		<T.MeshStandardMaterial color="#2f3339" roughness={0.9} metalness={0} />
	</T.Mesh>
	<T.Mesh position={[42.5, 0, -8]}>
		<T.BoxGeometry args={[119, 35, 7]} />
		<T.MeshStandardMaterial color="#9a9ea4" roughness={0.45} metalness={0.7} />
	</T.Mesh>
	<T.Mesh position={[42.5, 0, -4.4]}>
		<T.BoxGeometry args={[119, 25, 0.4]} />
		<T.MeshStandardMaterial color="#74787d" roughness={0.5} metalness={0.7} />
	</T.Mesh>

	<!-- AXC F 2152: bus base, housing, supply plug, two Ethernet sockets, the rail latch -->
	<T.Mesh position={[AXC.w / 2, 0, 6]}>
		<T.BoxGeometry args={[AXC.w, AXC.h, 16]} />
		<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
	</T.Mesh>
	<T.Mesh position={[AXC.w / 2, 0, (AXC.d + 10) / 2]}>
		<RoundedBoxGeometry args={[AXC.w - 0.6, AXC.h - 5, AXC.d - 10]} radius={1.6} />
		<T.MeshStandardMaterial color={GREY} roughness={0.7} />
	</T.Mesh>
	<Print w={AXC.w} h={AXC.h} position={[AXC.w / 2, 0, AXC.d + 0.08]} items={axcInk} paint={axcPaint} res={12} />
	<T.Mesh position={[11, AXC.h / 2 - 19, AXC.d + 3]}>
		<RoundedBoxGeometry args={[14, 24, 6]} radius={0.8} />
		<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
	</T.Mesh>
	<Print w={14} h={24} position={[11, AXC.h / 2 - 19, AXC.d + 6.08]} items={[]} paint={plugPaint} res={12} />
	{#each [80, 100] as y}
		<T.Mesh position={[13, AXC.h / 2 - y, AXC.d - 0.4]}>
			<T.BoxGeometry args={[16, 13.5, 1.2]} />
			<T.MeshStandardMaterial color="#b9bdc0" roughness={0.35} metalness={0.8} />
		</T.Mesh>
		<T.Mesh position={[13, AXC.h / 2 - y - 0.6, AXC.d + 0.25]}>
			<T.PlaneGeometry args={[12.4, 9.6]} />
			<T.MeshBasicMaterial color="#08090a" toneMapped={false} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[AXC.w / 2, -AXC.h / 2 - 1.5, 8]}>
		<T.BoxGeometry args={[7, 5, 9]} />
		<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
	</T.Mesh>
	{#each LAMPS as pair, i}
		{#each pair as name, j}
			{#if name}
				<Led
					position={[25.9 + j * 8.4, AXC.h / 2 - 10 - i * 4.6, AXC.d + 0.3]}
					size={[1.6, 1.6]}
					on={(name === 'UL' && live) || (name === 'D' && online)}
					off="#5d6265"
				/>
			{/if}
		{/each}
	{/each}

	<!-- AXL F BP SE4: the backplane. Its own lamps and supply terminals, down the left edge, are left off:
	     half hidden behind the controller they read as stray channel lamps -->
	<T.Mesh position={[BP.x + BP.w / 2, 0, BP.d / 2]}>
		<RoundedBoxGeometry args={[BP.w - 0.4, BP.h, BP.d]} radius={1} />
		<T.MeshStandardMaterial color={GREY} roughness={0.7} />
	</T.Mesh>
	<Print w={10} h={BP.h} position={[BP.x + 5, 0, BP.d + 0.08]} items={[{ text: 'AXL F BP SE4', x: 5.2, y: 26, size: 1.5, align: 'center', rotate: -90, color: INK, weight: 600, spacing: 0.1 }]} res={12} />
	<T.Mesh position={[BP.x + BP.w - 3, -BP.h / 2 - 1.5, 8]}>
		<T.BoxGeometry args={[6, 5, 9]} />
		<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
	</T.Mesh>

	{#each ELEMENTS as e (e.slot)}
		{@const x = seX(e)}
		{@const top = seTop(e)}
		<!-- one Smart Element: its housing, its face, its orange latch -->
		<T.Mesh position={[x + SE.w / 2, top - SE.h / 2, (SE.z0 + SE.z1) / 2]}>
			<RoundedBoxGeometry args={[SE.w, SE.h, SE.z1 - SE.z0]} radius={0.7} />
			<T.MeshStandardMaterial color={e.kind === 'cover' ? '#989d9f' : GREY} roughness={0.7} />
		</T.Mesh>
		<Print
			w={SE.w}
			h={SE.h}
			position={[x + SE.w / 2, top - SE.h / 2, SE.z1 + 0.08]}
			items={faceInk(e)}
			paint={e.kind === 'cover' ? pipes : terminals(e.kind === 'ai' ? 8 : 16)}
			res={16}
		/>
		<T.Mesh position={[x + 1.2, top - 32.4, SE.z1 + 0.5]}>
			<RoundedBoxGeometry args={[2.6, 3.6, 1.6]} radius={0.4} />
			<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
		</T.Mesh>

		{#if e.kind === 'do' || e.kind === 'di'}
			{#each { length: e.channels } as _, ch (ch)}
				{@const tag = tagOf(e, ch)}
				{@const y = top - rowAt(ch)}
				<Led position={[x + 1.6, y, SE.z1 + 0.3]} size={[1.5, 1.6]} on={on(tag)} color={YELLOW} off="#6a6f72" spread={3.4} />
				{#if hovered === tag}
					<T.Mesh position={[x + SE.w / 2, y, SE.z1 + 0.15]}>
						<T.PlaneGeometry args={[SE.w - 0.6, ROW]} />
						<T.MeshBasicMaterial color="#1a1a19" transparent opacity={0.22} depthWrite={false} toneMapped={false} />
					</T.Mesh>
				{/if}
				<T.Mesh position={[x + SE.w / 2, y, SE.z1 + 0.7]} onpointerenter={() => onhover?.(describe(e, ch))} onpointerleave={() => onhover?.(null)}>
					<T.PlaneGeometry args={[SE.w, ROW]} />
					<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
				</T.Mesh>
			{/each}
		{/if}
		{#if e.kind === 'ai'}
			<!-- E: the element's error lamp, lit while any input word is in the fault range -->
			<Led
				position={[x + 1.6, top - 5.3, SE.z1 + 0.3]}
				size={[1.5, 1.6]}
				on={live && Array.from({ length: e.channels }, (_, ch) => analogFault(raw(tagOf(e, ch)))).some(Boolean)}
				color="#ff3b30"
				off="#6a6f72"
				spread={3.4}
			/>
			{#each { length: e.channels } as _, ch (ch)}
				{@const tag = tagOf(e, ch)}
				{@const y = top - aiAt(ch)}
				{#if hovered === tag}
					<T.Mesh position={[x + SE.w / 2, y, SE.z1 + 0.15]}>
						<T.PlaneGeometry args={[SE.w - 0.6, 6]} />
						<T.MeshBasicMaterial color="#1a1a19" transparent opacity={0.22} depthWrite={false} toneMapped={false} />
					</T.Mesh>
				{/if}
				<T.Mesh position={[x + SE.w / 2, y, SE.z1 + 0.7]} onpointerenter={() => onhover?.(describe(e, ch))} onpointerleave={() => onhover?.(null)}>
					<T.PlaneGeometry args={[SE.w, 6]} />
					<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
				</T.Mesh>
			{/each}
			<T.Mesh position={[x + SE.w / 2, (top - SE.h + CALLOUT.y + CALLOUT.h / 2) / 2, SE.z1 - 4]}>
				<T.BoxGeometry args={[0.4, top - SE.h - (CALLOUT.y + CALLOUT.h / 2), 0.4]} />
				<T.MeshBasicMaterial color="#d8d6d0" toneMapped={false} />
			</T.Mesh>
			<Print w={CALLOUT.w} h={CALLOUT.h} position={[CALLOUT.x, CALLOUT.y, SE.z1 - 4]} items={calloutInk()} plate={{ fill: 'rgba(22,22,21,0.94)', stroke: '#55554f', radius: 2 }} res={14} />
		{/if}
	{/each}
</T.Group>
