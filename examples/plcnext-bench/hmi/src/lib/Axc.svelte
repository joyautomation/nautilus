<script lang="ts" module>
	// The bench PLCnext station, drawn from boxes in millimetres, as it
	// stands in the PLCnext Technology Starter Kit: a charcoal board leaning
	// back on two feet, a DIN rail across it, and on the rail a few
	// feed-through terminals, the AXC F 2152 and an AXL F BP SE4 backplane
	// holding four Axioline Smart Elements, two over two. Right of them on
	// the board, four pushbuttons (wired to DI16 inputs 01..04) and a 0..10 V
	// slider (the AI4 U's source). Sizes are the datasheets' (45 x 126.9 mm
	// controller, 40 x 122.4 mm backplane, 15 x 62 mm elements); faces and
	// the board's layout follow product photos.
	import type { Box3 } from './fit';

	/** World units per millimetre. */
	const S = 0.01;
	/** The board, mm, before centring. The rail's middle is y = 0, the controller's left edge x = 0. */
	const BOARD = { x0: -85, x1: 200, y0: -104, y1: 100, z: -14 };
	/** How far the board leans back on its feet, radians. */
	const TILT = 0.2;
	const CX = (BOARD.x0 + BOARD.x1) / 2;
	const CY = (BOARD.y0 + BOARD.y1) / 2;
	/** A box on the leaning board, as the upright box that contains it, in world units. */
	const box = (x0: number, x1: number, y0: number, y1: number, z0: number, z1: number): Box3 => {
		const ys: number[] = [];
		const zs: number[] = [];
		for (const y of [y0, y1])
			for (const z of [z0, z1]) {
				const Y = y - BOARD.y0;
				const Z = z - BOARD.z;
				ys.push(BOARD.y0 + Y * Math.cos(TILT) + Z * Math.sin(TILT));
				zs.push(BOARD.z - Y * Math.sin(TILT) + Z * Math.cos(TILT));
			}
		return {
			min: [(x0 - CX) * S, (Math.min(...ys) - CY) * S, Math.min(...zs) * S],
			max: [(x1 - CX) * S, (Math.max(...ys) - CY) * S, Math.max(...zs) * S]
		};
	};
	/** What the camera keeps in frame in a wide or square window: the whole board on its feet. */
	/** The feet stand flat, so their box is not leaned. */
	const FEET: Box3 = { min: [(BOARD.x0 + 28 - CX) * S, (BOARD.y0 - 10 - CY) * S, (BOARD.z - 38) * S], max: [(BOARD.x1 - 28 - CX) * S, (BOARD.y0 + 4 - CY) * S, (BOARD.z + 66) * S] };
	export const BOUNDS: Box3[] = [FEET, box(BOARD.x0, BOARD.x1, BOARD.y0, BOARD.y1, -15, -11), box(-20, 85, -64, 64, 0, 76), box(35, 89, -87, -69, 50, 50)];
	/** And in a tall one, where the board may run off the sides: the station, the buttons and the slider. */
	export const BOUNDS_CORE: Box3[] = [box(-22, 180, -70, 68, -11, 20), box(-20, 85, -64, 64, 0, 76), box(35, 89, -87, -69, 50, 50)];

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
	import { CatmullRomCurve3, TubeGeometry, Vector3 } from 'three';
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

	// The controller's own lamps, two columns in the middle of its face.
	// Only D is driven here (the local bus is running, from the driver's
	// status), and UL by the supply plug (it is powered: it is serving this
	// page); the rest are drawn dark.
	const LAMPS = [
		['BF-C', 'RUN'],
		['BF-D', 'FAIL'],
		['SF', 'DBG'],
		['BOOT', 'D'],
		['EXT', 'E']
	];
	const LAMP = { x: 28.4, dx: 7.8, y: 58, dy: 3.9 };
	const axcInk: Ink[] = [
		...LAMPS.flatMap((pair, i) => pair.map((text, j): Ink => ({ text, x: LAMP.x + 1.5 + j * LAMP.dx, y: LAMP.y + i * LAMP.dy, size: 1.05, color: INK, weight: 600 }))),
		{ text: 'AXC F 2152', x: 25.6, y: 28, size: 2.1, align: 'center', rotate: -90, color: INK, weight: 650, spacing: 0.25 },
		{ text: 'SD', x: 31, y: 11, size: 1.3, color: INK, weight: 600 },
		{ text: 'X1', x: 37.5, y: 97, size: 1.5, color: INK, weight: 600 },
		{ text: 'X2', x: 37.5, y: 108, size: 1.5, color: INK, weight: 600 },
		{ text: 'UL', x: 17.8, y: 101, size: 1.2, color: INK, weight: 600 }
	];
	const axcPaint: Paint = (ctx) => {
		// two fields of ventilation slots down the left of the face
		ctx.fillStyle = '#4a4f52';
		for (const [y0, y1] of [[8, 50], [57, 92]])
			for (const x of [5, 9.6, 14.2, 18.8]) {
				ctx.beginPath();
				ctx.roundRect(x, y0, 2.1, y1 - y0, 1);
				ctx.fill();
			}
		// the white label fields: the SD slot's, upper right, and the Ethernet one, lower right
		ctx.fillStyle = '#eef0f0';
		ctx.beginPath();
		ctx.roundRect(29, 6, 13.5, 44, 0.8);
		ctx.fill();
		ctx.beginPath();
		ctx.roundRect(27.5, 82, 15, 40, 0.8);
		ctx.fill();
		ctx.fillStyle = '#1a1c1e';
		ctx.fillRect(35, 14, 1.3, 13);
		ctx.beginPath();
		ctx.arc(32.6, 16, 0.8, 0, Math.PI * 2);
		ctx.fill();
		// the lamp field's ground
		ctx.fillStyle = '#878c8e';
		ctx.beginPath();
		ctx.roundRect(27.2, 54.6, 16.2, 20.6, 0.6);
		ctx.fill();
		// the two socket marks: the sockets themselves open downward
		ctx.strokeStyle = '#2a2d30';
		ctx.lineWidth = 0.35;
		for (const y of [97, 108]) ctx.strokeRect(30.5, y - 3, 5.4, 5.4);
	};
	const plugPaint: Paint = (ctx) => {
		for (const [i, y] of [5, 12, 19, 26].entries()) {
			ctx.fillStyle = i % 2 ? '#2f6fd6' : '#d83a30';
			ctx.fillRect(1.4, y - 1.6, 2.6, 2.6);
			ctx.fillStyle = '#000';
			ctx.beginPath();
			ctx.arc(7, y, 1.7, 0, Math.PI * 2);
			ctx.fill();
		}
	};

	// The board's own parts. Buttons top to bottom are DI16 inputs 01..04
	// (DI_00..DI_03); the slider is the AI4 U's 0..10 V source, read off AI_00.
	const BUTTONS = { x: 126, ys: [50, 17, -16, -49] };
	const SLIDER = { x: 166, y0: -16, y1: 50 };
	/** An Axioline analog input in its standard format reads 0..30000 counts over 0..10 V. */
	const FULL_SCALE = 30000;
	let slide = $derived.by(() => {
		const v = raw('AI_00');
		if (typeof v !== 'number' || analogFault(v)) return null;
		return Math.min(1, Math.max(0, v / FULL_SCALE));
	});
	const scaleInk: Ink[] = [
		{ text: '10V', x: 1, y: 3, size: 2.6, color: '#e3e4e4', weight: 500 },
		{ text: '0V', x: 3.2, y: 79, size: 2.6, color: '#e3e4e4', weight: 500 }
	];
	const scalePaint: Paint = (ctx) => {
		ctx.strokeStyle = '#e3e4e4';
		ctx.lineWidth = 0.4;
		ctx.beginPath();
		ctx.moveTo(8, 8);
		ctx.lineTo(8, 74);
		for (let i = 0; i <= 10; i++) {
			ctx.moveTo(8, 8 + i * 6.6);
			ctx.lineTo(i % 5 === 0 ? 13 : 11, 8 + i * 6.6);
		}
		ctx.stroke();
	};

	/** A wire: a tube through the given points, mm. */
	const wire = (pts: [number, number, number][], r: number) => new TubeGeometry(new CatmullRomCurve3(pts.map((p) => new Vector3(...p))), 40, r, 6);
	const WIRES = [
		// the feed-through terminals to the controller's supply plug
		{ color: '#c9302a', g: wire([[-13, -34, 34], [-15, -58, 40], [-12, -78, 56], [-2, -80, 78], [7, -68, 86], [7, -56, 84]], 0.9) },
		{ color: '#2a5fc4', g: wire([[-8, -34, 34], [-10, -54, 40], [-8, -70, 56], [1, -72, 78], [10, -62, 88], [10, -50, 84]], 0.9) },
		// the plug on down through the board
		{ color: '#c9302a', g: wire([[7, -44, 84], [1, -50, 92], [-4, -76, 60], [4, -98, 10], [4, -100, -12]], 0.9) },
		{ color: '#2a5fc4', g: wire([[10, -38, 84], [5, -46, 95], [1, -74, 64], [9, -98, 10], [9, -100, -12]], 0.9) },
		// the lower elements' bundles: the inputs to the buttons, the analog pair to the slider
		{ color: '#8d9194', g: wire([[61, -48, 55], [58, -64, 46], [50, -84, 22], [42, -99, -4], [42, -100, -12]], 1.7) },
		{ color: '#8d9194', g: wire([[77, -22, 55], [84, -40, 60], [80, -66, 44], [56, -90, 14], [46, -100, -12]], 1.3) }
	];
</script>

<T.Group scale={S} position={[-CX * S, -CY * S, 0]}>
	<!-- the two feet the board stands in -->
	{#each [BOARD.x0 + 42, BOARD.x1 - 42] as x}
		<T.Mesh position={[x, BOARD.y0 - 3, BOARD.z + 14]}>
			<RoundedBoxGeometry args={[28, 14, 104]} radius={1.5} />
			<T.MeshStandardMaterial color="#0b0b0c" roughness={0.6} metalness={0} />
		</T.Mesh>
	{/each}

	<T.Group position={[0, BOARD.y0, BOARD.z]} rotation={[-TILT, 0, 0]}>
	<T.Group position={[0, -BOARD.y0, -BOARD.z]}>
	<!-- the board, its four bolts, the power jack, and the DIN rail with its end clamp -->
	<T.Mesh position={[CX, CY, BOARD.z + 1.5]}>
		<RoundedBoxGeometry args={[BOARD.x1 - BOARD.x0, BOARD.y1 - BOARD.y0, 3]} radius={1.2} />
		<T.MeshStandardMaterial color="#131416" roughness={0.9} metalness={0} />
	</T.Mesh>
	{#each [[BOARD.x0 + 16, BOARD.y1 - 14], [BOARD.x1 - 16, BOARD.y1 - 14], [BOARD.x0 + 16, BOARD.y0 + 22], [BOARD.x1 - 16, BOARD.y0 + 22]] as [x, y]}
		<T.Mesh position={[x, y, BOARD.z + 4.2]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[3.6, 3.6, 2.6, 6]} />
			<T.MeshStandardMaterial color="#6c7074" roughness={0.6} metalness={0.2} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[BOARD.x0 + 34, BOARD.y0 + 22, BOARD.z + 4]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[4.2, 4.2, 2.4, 20]} />
		<T.MeshStandardMaterial color="#6c7074" roughness={0.6} metalness={0.2} />
	</T.Mesh>
	<T.Mesh position={[BOARD.x0 + 34, BOARD.y0 + 22, BOARD.z + 5.3]}>
		<T.CircleGeometry args={[2.4, 20]} />
		<T.MeshBasicMaterial color="#050506" toneMapped={false} />
	</T.Mesh>
	<T.Mesh position={[15, 0, -8]}>
		<T.BoxGeometry args={[170, 35, 7]} />
		<T.MeshStandardMaterial color="#a4a8ad" roughness={0.4} metalness={0.75} />
	</T.Mesh>
	<T.Mesh position={[15, 0, -4.4]}>
		<T.BoxGeometry args={[170, 25, 0.4]} />
		<T.MeshStandardMaterial color="#7d8186" roughness={0.5} metalness={0.75} />
	</T.Mesh>
	<T.Mesh position={[91, 0, 2]}>
		<RoundedBoxGeometry args={[9, 44, 18]} radius={1.2} />
		<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
	</T.Mesh>

	<!-- three feed-through terminal blocks, orange levers up and down their faces -->
	{#each [-19, -13, -7] as x, i}
		<T.Mesh position={[x + 2.8, 0, 17]}>
			<RoundedBoxGeometry args={[5.4, 70 - i * 3, 34]} radius={1} />
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
		{#each [22, 14, -14, -22] as y}
			<T.Mesh position={[x + 2.8, y, 34.4]}>
				<T.BoxGeometry args={[3.4, 3.2, 1.6]} />
				<T.MeshBasicMaterial color={i === 2 && y === -14 ? '#c9302a' : ORANGE} toneMapped={false} />
			</T.Mesh>
		{/each}
	{/each}

	<!-- the wiring -->
	{#each WIRES as w}
		<T.Mesh geometry={w.g}>
			<T.MeshStandardMaterial color={w.color} roughness={0.55} />
		</T.Mesh>
	{/each}

	<!-- the four pushbuttons, DI16 inputs 01..04: a pressed one sits in and its collar lights -->
	{#each BUTTONS.ys as y, i}
		{@const tag = `DI_0${i}`}
		{@const down = on(tag)}
		<T.Mesh position={[BUTTONS.x, y, BOARD.z + 4.6]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[5.4, 5.4, 3.2, 6]} />
			<T.MeshStandardMaterial color="#6c7074" roughness={0.6} metalness={0.2} />
		</T.Mesh>
		<T.Mesh position={[BUTTONS.x, y, BOARD.z + 6.4]}>
			<T.TorusGeometry args={[4.1, 0.7, 8, 28]} />
			<T.MeshBasicMaterial color={down ? YELLOW : '#3a3d40'} toneMapped={false} />
		</T.Mesh>
		<T.Mesh position={[BUTTONS.x, y, BOARD.z + (down ? 7.5 : 10.5)]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[3.2, 3.2, down ? 4 : 10, 20]} />
			<T.MeshStandardMaterial color="#050506" roughness={0.6} />
		</T.Mesh>
		{#if hovered === tag}
			<T.Mesh position={[BUTTONS.x, y, BOARD.z + 3.2]}>
				<T.CircleGeometry args={[9, 28]} />
				<T.MeshBasicMaterial color="#ffffff" transparent opacity={0.16} depthWrite={false} toneMapped={false} />
			</T.Mesh>
		{/if}
		<T.Mesh position={[BUTTONS.x, y, BOARD.z + 16]} onpointerenter={() => onhover?.({ tag, text: () => (on(tag) ? 'on' : 'off') })} onpointerleave={() => onhover?.(null)}>
			<T.PlaneGeometry args={[18, 18]} />
			<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
		</T.Mesh>
	{/each}

	<!-- the slider: its printed scale, its slot, and the knob, moved by AI_00;
	     parked at 0 V and greyed while the input reports a fault instead of a reading -->
	<Print w={16} h={82} position={[SLIDER.x - 15, (SLIDER.y0 + SLIDER.y1) / 2, BOARD.z + 3.1]} items={scaleInk} paint={scalePaint} res={12} />
	<T.Mesh position={[SLIDER.x, (SLIDER.y0 + SLIDER.y1) / 2, BOARD.z + 3.2]}>
		<T.BoxGeometry args={[2.2, SLIDER.y1 - SLIDER.y0 + 8, 0.6]} />
		<T.MeshBasicMaterial color="#050506" toneMapped={false} />
	</T.Mesh>
	<T.Group position={[SLIDER.x, SLIDER.y0 + (slide ?? 0) * (SLIDER.y1 - SLIDER.y0), BOARD.z + 9]}>
		<T.Mesh>
			<RoundedBoxGeometry args={[13, 20, 12]} radius={1.6} />
			<T.MeshStandardMaterial color={slide === null ? '#4a4d50' : '#151617'} roughness={0.5} />
		</T.Mesh>
		<T.Mesh position={[0, 0, 6.1]}>
			<T.PlaneGeometry args={[13, 0.9]} />
			<T.MeshBasicMaterial color={slide === null ? '#8a8d90' : '#f2f3f3'} toneMapped={false} />
		</T.Mesh>
	</T.Group>

	<!-- AXC F 2152: bus base, housing, the supply plug lower left, two Ethernet sockets underneath, the rail latch -->
	<T.Mesh position={[AXC.w / 2, 0, 6]}>
		<T.BoxGeometry args={[AXC.w, AXC.h, 16]} />
		<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
	</T.Mesh>
	<T.Mesh position={[AXC.w / 2, 0, (AXC.d + 10) / 2]}>
		<RoundedBoxGeometry args={[AXC.w - 0.6, AXC.h - 5, AXC.d - 10]} radius={1.6} />
		<T.MeshStandardMaterial color={GREY} roughness={0.7} />
	</T.Mesh>
	<Print w={AXC.w} h={AXC.h} position={[AXC.w / 2, 0, AXC.d + 0.08]} items={axcInk} paint={axcPaint} res={12} />
	<T.Mesh position={[8.5, AXC.h / 2 - 110, AXC.d + 4]}>
		<RoundedBoxGeometry args={[11, 30, 8]} radius={0.8} />
		<T.MeshStandardMaterial color="#1c1d1f" roughness={0.6} />
	</T.Mesh>
	<Print w={11} h={30} position={[8.5, AXC.h / 2 - 110, AXC.d + 8.08]} items={[]} paint={plugPaint} res={12} />
	{#each [20, 34] as x}
		<T.Mesh position={[x, -AXC.h / 2 + 2.4, AXC.d - 14]} rotation={[Math.PI / 2, 0, 0]}>
			<T.PlaneGeometry args={[11, 13]} />
			<T.MeshBasicMaterial color="#08090a" toneMapped={false} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[AXC.w / 2, -AXC.h / 2 - 1.5, 8]}>
		<T.BoxGeometry args={[7, 5, 9]} />
		<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
	</T.Mesh>
	{#each LAMPS as pair, i}
		{#each pair as name, j}
			<Led position={[LAMP.x + j * LAMP.dx, AXC.h / 2 - LAMP.y - i * LAMP.dy, AXC.d + 0.3]} size={[1.4, 1.4]} on={name === 'D' && online} off="#5d6265" />
		{/each}
	{/each}
	<Led position={[16.4, AXC.h / 2 - 101, AXC.d + 0.3]} size={[1.4, 1.4]} on={live} off="#5d6265" />

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
	</T.Group>
</T.Group>
