<script lang="ts" module>
	// The bench PLCnext station, drawn from boxes in millimetres, as it
	// stands on the desk: the PLCnext Technology Starter Kit's black board
	// leaning back in two feet, a DIN rail across it, and on the rail a block
	// of feed-through terminals, the AXC F 2152 and an AXL F BP SE4 backplane
	// holding four Axioline Smart Elements, two over two. Right of them on
	// the board, four momentary pushbuttons labelled S4 (top) to S1 (bottom), wired
	// to DI16 inputs 04..01, and a 0..10 V slider, the AI4 U's source. Sizes
	// are the datasheets' (45 x 126.9 mm controller, 40 x 122.4 mm backplane,
	// 15 x 62 mm elements); the board (360 x 265 mm, leaning 21 degrees) is
	// measured off a photogrammetry capture of the bench, and the faces and
	// positions follow photographs of it.
	import type { Box3 } from './fit';

	/** World units per millimetre. */
	const S = 0.01;
	/** The board, mm, before centring. The rail's middle is y = 0, the controller's left edge x = 0. */
	const BOARD = { x0: -97, x1: 263, y0: -140, y1: 125, z: -14 };
	/** How far the board leans back in its feet, radians. */
	const TILT = 0.365;
	const CX = (BOARD.x0 + BOARD.x1) / 2;
	const CY = -30;
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
	/** What the camera keeps in frame in a wide or square window: the whole board, and the station standing off it. */
	export const BOUNDS: Box3[] = [box(BOARD.x0, BOARD.x1, BOARD.y0 - 18, BOARD.y1, -14, -11), box(BOARD.x1 - 40, BOARD.x1 - 12, BOARD.y0 - 6, BOARD.y0 + 16, -14, 40), box(-27, 85, -64, 64, 0, 76)];
	/** And in a tall one, where the board may run off the sides: the station, the switches and the slider. */
	export const BOUNDS_CORE: Box3[] = [box(-30, 232, -92, 70, -11, 20), box(-27, 85, -64, 64, 0, 76)];

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
	import { getContext, onMount } from 'svelte';
	import { CatmullRomCurve3, Mesh, TubeGeometry, Vector3, type BufferGeometry } from 'three';
	import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js';
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

	// The bodies are hand-drawn below, from photographs: there is no
	// published model of these parts in the repo. To replace one with a
	// model, put a GLB in static/models (one mesh, in this component's frame
	// in metres: x from the controller's left edge, y up from the rail's
	// middle, z out from the rail; the element model in slot 1's place) and
	// name it here. A body with a model loaded is drawn from it and its
	// hand-drawn housing is skipped; the lamps, lettering, wiring and
	// everything bound to a tag stay as they are, on top.
	type Body = 'controller' | 'backplane' | 'element';
	const MODELS: Partial<Record<Body, string>> = {};
	let bodies = $state<Partial<Record<Body, BufferGeometry>>>({});
	onMount(() => {
		const loader = new GLTFLoader();
		for (const [name, url] of Object.entries(MODELS) as [Body, string][])
			loader.load(url, (gltf) => {
				gltf.scene.traverse((o) => {
					if (o instanceof Mesh) bodies[name] = o.geometry;
				});
			});
	});

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

	/** A field of push-in terminals: the points alternate left and right down the element, each with its release beside it. */
	const terminals =
		(n: number): Paint =>
		(ctx) => {
			ctx.fillStyle = '#7b8082';
			ctx.beginPath();
			ctx.roundRect(5.3, 6.2, 7.4, n * 3 + 0.8, 0.5);
			ctx.fill();
			for (let k = 0; k < n; k++) {
				const y = 6.6 + k * 3 + 1.5;
				const leftward = k % 2 === 0;
				ctx.fillStyle = '#d9dcdd';
				ctx.fillRect(leftward ? 10 : 6, y - 0.9, 1.9, 1.8);
				ctx.fillStyle = '#0c0d0e';
				ctx.beginPath();
				ctx.arc(leftward ? 7.4 : 10.6, y, 1.3, 0, Math.PI * 2);
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
	const CALLOUT = { w: 54, h: 17.5, x: 99, y: -80 };

	// The controller's own lamps: two columns in the middle of its face, the
	// names outside them. RUN is drawn lit while frames arrive (the
	// controller is up: it is serving this page) and D from the driver's
	// status (the local bus is running); the rest are drawn dark.
	const LAMPS = [
		['BF-C', 'RUN'],
		['BF-D', 'FAIL'],
		['SF', 'DBG'],
		['BOOT', 'D'],
		['EXT', 'E']
	];
	const LAMP = { x: 27.2, dx: 4.6, y: 55.4, dy: 3.5 };
	const axcInk: Ink[] = [
		...LAMPS.flatMap((pair, i): Ink[] => [
			{ text: pair[0], x: LAMP.x - 1.6, y: LAMP.y + i * LAMP.dy, size: 1, align: 'right', color: INK, weight: 600 },
			{ text: pair[1], x: LAMP.x + LAMP.dx + 1.6, y: LAMP.y + i * LAMP.dy, size: 1, color: INK, weight: 600 }
		]),
		{ text: 'PLCnext Control', x: 7, y: 26, size: 1.9, align: 'center', rotate: -90, color: INK, weight: 500, spacing: 0.2 },
		{ text: 'AXC F 2152', x: 11.2, y: 26, size: 1.9, align: 'center', rotate: -90, color: INK, weight: 650, spacing: 0.3 },
		{ text: 'Ord.-No. 2404267', x: 15.2, y: 26, size: 1.4, align: 'center', rotate: -90, color: INK, weight: 500, spacing: 0.1 }
	];
	const axcPaint: Paint = (ctx) => {
		// the seams between the face's three sections
		ctx.strokeStyle = '#5d6265';
		ctx.lineWidth = 0.35;
		for (const y of [47.5, 77.5]) {
			ctx.beginPath();
			ctx.moveTo(y > 60 ? STEP.w : 1.5, y);
			ctx.lineTo(43.5, y);
			ctx.stroke();
		}
		// the two white label fields
		ctx.fillStyle = '#eef0f0';
		ctx.beginPath();
		ctx.roundRect(19.5, 5, 22, 41, 0.8);
		ctx.fill();
		ctx.beginPath();
		ctx.roundRect(21, 80, 20.5, 41, 0.8);
		ctx.fill();
		// the two socket marks and the arrow to the sockets underneath
		ctx.strokeStyle = '#2a2d30';
		ctx.lineWidth = 0.3;
		for (const y of [98, 106]) ctx.strokeRect(37.4, y - 2.2, 3.4, 4.2);
		ctx.beginPath();
		ctx.moveTo(39.1, 111.5);
		ctx.lineTo(39.1, 116);
		ctx.moveTo(37.9, 114.6);
		ctx.lineTo(39.1, 116);
		ctx.lineTo(40.3, 114.6);
		ctx.stroke();
	};
	/** The controller's left side: two fields of ventilation slots. */
	const axcSide: Paint = (ctx) => {
		ctx.fillStyle = '#3d4144';
		for (const [y0, y1] of [[8, 54], [62, 104]])
			for (const x of [16, 24, 32, 40]) {
				ctx.beginPath();
				ctx.roundRect(x, y0, 3.2, y1 - y0, 1.2);
				ctx.fill();
			}
	};
	/** The lower-left corner of the controller's face steps back; the supply plug sits inside the step. */
	const STEP = { w: 19.5, top: 77.5, depth: 17 };
	/** The step's back wall: sloped slots over the plug. */
	const stepPaint: Paint = (ctx) => {
		ctx.fillStyle = '#3d4144';
		for (const y of [3, 9.5, 16]) {
			ctx.beginPath();
			ctx.roundRect(2.5, y, 14, 3.4, 0.8);
			ctx.fill();
		}
	};
	const plugPaint: Paint = (ctx) => {
		for (const [i, x] of [3, 9].entries())
			for (const y of [5, 12, 19]) {
				ctx.fillStyle = y === 5 ? '#5a5f63' : i ? '#2f6fd6' : '#d83a30';
				ctx.fillRect(x - 1.5, y - 2.6, 3, 2);
				ctx.fillStyle = '#000';
				ctx.beginPath();
				ctx.arc(x, y + 1, 1.5, 0, Math.PI * 2);
				ctx.fill();
			}
	};
	/** The feed-through terminals' faces: four across, two rows of points with orange levers above and below a white marker strip. */
	const TERM = { x0: -21, w: 21, h: 58, d: 46 };
	const termPaint: Paint = (ctx) => {
		for (let c = 0; c < 4; c++) {
			const x = 2.7 + c * 5.2;
			for (const y of [5, 13.5, 44, 52.5]) {
				ctx.fillStyle = '#0c0d0e';
				ctx.beginPath();
				ctx.arc(x, y, 1.7, 0, Math.PI * 2);
				ctx.fill();
				ctx.fillStyle = '#ef7d00';
				ctx.fillRect(x - 1.7, y + 2.3, 3.4, 2.2);
			}
		}
		ctx.fillStyle = '#eef0f0';
		ctx.fillRect(0.6, 19.5, 19.8, 9.5);
		ctx.fillStyle = '#c9302a';
		ctx.fillRect(1.6, 31, 7.6, 3.6);
		ctx.fillRect(11.6, 35, 7.6, 3.6);
	};
	/** The backplane's own strip, left of the elements: a white marker, its lamps' window, its supply points. */
	const bpPaint: Paint = (ctx) => {
		ctx.fillStyle = '#eef0f0';
		ctx.beginPath();
		ctx.roundRect(2.2, 16, 5.6, 12, 0.6);
		ctx.fill();
		ctx.fillStyle = '#0c0d0e';
		for (const y of [78, 85, 92, 99]) {
			ctx.beginPath();
			ctx.arc(5, y, 1.5, 0, Math.PI * 2);
			ctx.fill();
		}
	};
	/** The backplane's lamps, green on the bench unit: lit here while the driver is exchanging the bus. */
	const BP_LAMPS: [number, number][] = [[3.7, 58], [6.3, 58], [3.7, 63.6], [6.3, 63.6], [5, 69]];

	// The board's own parts. The pushbuttons' labels read S4, S3, S2, S1 from
	// the top, so the bottom one is DI16 input 01 (DI_00) and the top one
	// input 04 (DI_03); they are momentary, on while held. The slider is the AI4 U's 0..10 V source, read off AI_00.
	const SWITCHES = { x: 167, ys: [24, 1, -21, -45] };
	const switchTag = (i: number) => `DI_0${3 - i}`;
	const SLIDER = { x: 217, y0: -28, y1: 15 };
	/** An Axioline analog input in its standard format reads 0..30000 counts over 0..10 V. */
	const FULL_SCALE = 30000;
	let slide = $derived.by(() => {
		const v = raw('AI_00');
		if (typeof v !== 'number' || analogFault(v)) return null;
		return Math.min(1, Math.max(0, v / FULL_SCALE));
	});
	const scaleInk: Ink[] = [
		{ text: '10V', x: 5.6, y: 2.4, size: 2.3, align: 'center', color: '#e3e4e4', weight: 600 },
		{ text: '0V', x: 9, y: 61.6, size: 2.3, align: 'center', color: '#e3e4e4', weight: 600 }
	];
	const scalePaint: Paint = (ctx) => {
		ctx.strokeStyle = '#e3e4e4';
		ctx.lineWidth = 0.45;
		ctx.beginPath();
		ctx.moveTo(5, 6);
		ctx.lineTo(5, 58);
		for (let i = 0; i <= 8; i++) {
			ctx.moveTo(5, 6 + i * 6.5);
			ctx.lineTo(i % 4 === 0 ? 12 : 9, 6 + i * 6.5);
		}
		ctx.stroke();
	};
	/** The board's printed lines, plain: no lettering, no marks. */
	const BW = BOARD.x1 - BOARD.x0;
	const BH = BOARD.y1 - BOARD.y0;
	const boardLines: Paint = (ctx) => {
		ctx.strokeStyle = 'rgba(214,216,218,0.55)';
		ctx.lineWidth = 0.5;
		const line = (pts: [number, number][]) => {
			ctx.beginPath();
			pts.forEach(([x, y], i) => (i ? ctx.lineTo(x, y) : ctx.moveTo(x, y)));
			ctx.stroke();
		};
		line([[44, 132], [44, 62], [70, 36], [96, 36]]);
		line([[30, 250], [30, 196], [52, 174], [96, 174]]);
		line([[34, 250], [34, 198], [54, 178], [96, 178]]);
		line([[186, 36], [236, 36], [222, 70], [186, 104]]);
		line([[186, 172], [226, 172], [238, 184]]);
		line([[186, 112], [222, 112]]);
	};

	// In the leaning frame: where the desk is under a point, and the leads that run down to it.
	const deskY = (z: number) => BOARD.y0 - 6 - (z - BOARD.z) * Math.tan(TILT);
	const onDesk = (x: number, z: number): [number, number, number] => [x, deskY(z) + 3, z];
	/** A wire: a tube through the given points, mm. */
	const wire = (pts: [number, number, number][], r: number) => new TubeGeometry(new CatmullRomCurve3(pts.map((p) => new Vector3(...p))), 48, r, 6);
	const HOLES = [-12, 8, 52, 74];
	const HOLE_Y = -100;
	const down = (x: number, y: number, z: number, hole: number, dx: number, bulge: number): [number, number, number][] => [
		[x, y, z],
		[x, y - 14, z + bulge],
		[HOLES[hole] + dx, HOLE_Y + 16, 10],
		[HOLES[hole] + dx, HOLE_Y, -12]
	];
	const RED = '#c9302a';
	const BLUE = '#2a5fc4';
	const GREY_WIRE = '#9a9ea2';
	const WIRES = [
		// the feed-through terminals, down through the board
		...[0, 1, 2, 3].map((c) => ({ color: c < 2 ? RED : BLUE, g: wire(down(TERM.x0 + 2.7 + c * 5.2, -27, TERM.d - 2, 0, c * 1.4 - 2, 4), 0.85) })),
		// the controller's supply plug
		{ color: RED, g: wire(down(7.4, -50, AXC.d - 3, 1, -2, 8), 0.85) },
		{ color: BLUE, g: wire(down(13.4, -50, AXC.d - 3, 1, 0, 8), 0.85) },
		{ color: RED, g: wire(down(7.4, -57, AXC.d - 3, 1, -1, 12), 0.85) },
		{ color: BLUE, g: wire(down(13.4, -57, AXC.d - 3, 1, 1, 12), 0.85) },
		// the backplane's supply
		{ color: RED, g: wire(down(50, -24, 37, 2, -1, 6), 0.85) },
		{ color: BLUE, g: wire(down(50, -31, 37, 2, 1, 6), 0.85) },
		// the DI16's four inputs, to the switches, and the AI4 U's pair, to the slider: out, over and down as one bundle
		...[0, 1, 2, 3].map((i) => ({
			color: GREY_WIRE,
			g: wire([[65, -8 - i * 3, 55], [70 + i, -8 - i * 3, 64], [84 + i * 1.5, -30, 62], [88 + i, -66, 40], [HOLES[3] + i * 1.3, HOLE_Y + 12, 8], [HOLES[3] + i * 1.3, HOLE_Y, -12]], 0.9)
		})),
		...[0, 1].map((i) => ({
			color: BLUE,
			g: wire([[80, -8 - i * 3, 55], [86 + i, -10 - i * 3, 62], [93 + i, -34, 56], [94 + i, -68, 36], [HOLES[3] + 6 + i * 1.3, HOLE_Y + 12, 8], [HOLES[3] + 6 + i * 1.3, HOLE_Y, -12]], 0.9)
		})),
		// Ethernet, from under the controller, and the supply lead from the board's jack
		{ color: '#1d4fb8', g: wire([[31, -62, 58], [31, -84, 62], [26, -120, 76], onDesk(8, 110), onDesk(-70, 190), onDesk(-190, 250)], 2.4) },
		{ color: '#101112', g: wire([[-46, -100, -6], [-48, -102, 14], [-56, -118, 38], onDesk(-82, 70), onDesk(-150, 120), onDesk(-250, 130)], 1.7) }
	];
</script>

<T.Group scale={S} position={[-CX * S, -CY * S, 0]}>
	<!-- the desk, and the two feet the board stands in -->
	<T.Mesh position={[CX, BOARD.y0 - 11, 70]}>
		<RoundedBoxGeometry args={[760, 10, 560]} radius={2} />
		<T.MeshStandardMaterial color="#b6935c" roughness={0.75} metalness={0} />
	</T.Mesh>
	{#each [BOARD.x0 + 34, BOARD.x1 - 26] as x}
		<T.Mesh position={[x, BOARD.y0 + 5, BOARD.z - 58]}>
			<RoundedBoxGeometry args={[26, 22, 172]} radius={1.5} />
			<T.MeshStandardMaterial color="#0b0b0c" roughness={0.6} metalness={0} envMapIntensity={0.3} />
		</T.Mesh>
	{/each}

	<T.Group position={[0, BOARD.y0, BOARD.z]} rotation={[-TILT, 0, 0]}>
	<T.Group position={[0, -BOARD.y0, -BOARD.z]}>
	<!-- the board, its printed lines, its four bolts and the supply jack -->
	<T.Mesh position={[CX, (BOARD.y0 + BOARD.y1) / 2, BOARD.z + 1.5]}>
		<RoundedBoxGeometry args={[BW, BH, 3]} radius={1.2} />
		<T.MeshStandardMaterial color="#0d0e0f" roughness={0.95} metalness={0} envMapIntensity={0.25} />
	</T.Mesh>
	<Print w={BW} h={BH} position={[CX, (BOARD.y0 + BOARD.y1) / 2, BOARD.z + 3.06]} items={[]} paint={boardLines} res={5} />
	{#each [[BOARD.x0 + 25, BOARD.y1 - 25], [BOARD.x1 - 25, BOARD.y1 - 25], [BOARD.x0 + 25, BOARD.y0 + 36], [BOARD.x1 - 22, BOARD.y0 + 46]] as [x, y]}
		<T.Mesh position={[x, y, BOARD.z + 6]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[2.4, 4, 7, 16]} />
			<T.MeshStandardMaterial color="#0b0b0c" roughness={0.5} envMapIntensity={0.4} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[-46, -100, BOARD.z + 5]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[4.2, 4.2, 4, 20]} />
		<T.MeshStandardMaterial color="#7f8387" roughness={0.45} metalness={0.6} />
	</T.Mesh>
	{#each HOLES as x}
		<T.Mesh position={[x, HOLE_Y, BOARD.z + 3.6]}>
			<T.TorusGeometry args={[4.6, 1.5, 8, 24]} />
			<T.MeshStandardMaterial color="#050506" roughness={0.6} />
		</T.Mesh>
	{/each}

	<!-- the DIN rail, clamped at both ends, running on past the elements -->
	<T.Mesh position={[34, 0, -8]}>
		<T.BoxGeometry args={[206, 35, 7]} />
		<T.MeshStandardMaterial color="#a4a8ad" roughness={0.4} metalness={0.75} />
	</T.Mesh>
	<T.Mesh position={[34, 0, -4.4]}>
		<T.BoxGeometry args={[206, 25, 0.4]} />
		<T.MeshStandardMaterial color="#7d8186" roughness={0.5} metalness={0.75} />
	</T.Mesh>
	{#each [-70, 138] as x}
		<T.Mesh position={[x, 0, -6]}>
			<RoundedBoxGeometry args={[5, 40, 11]} radius={1} />
			<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
		</T.Mesh>
	{/each}

	<!-- the feed-through terminals, and the grey end bracket on their left -->
	<T.Mesh position={[TERM.x0 + TERM.w / 2, 0, TERM.d / 2]}>
		<RoundedBoxGeometry args={[TERM.w, TERM.h, TERM.d]} radius={1} />
		<T.MeshStandardMaterial color={GREY} roughness={0.7} />
	</T.Mesh>
	<Print w={TERM.w} h={TERM.h} position={[TERM.x0 + TERM.w / 2, 0, TERM.d + 0.08]} items={[]} paint={termPaint} res={12} />
	<T.Mesh position={[TERM.x0 - 3.2, -2, 15]}>
		<RoundedBoxGeometry args={[6, 46, 32]} radius={1} />
		<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
	</T.Mesh>

	<!-- the wiring -->
	{#each WIRES as w}
		<T.Mesh geometry={w.g}>
			<T.MeshStandardMaterial color={w.color} roughness={0.55} />
		</T.Mesh>
	{/each}

	<!-- the four pushbuttons, S4 at the top to S1 at the bottom, momentary: a pressed
	     one sits in, and its collar lights so it reads from across the room -->
	{#each SWITCHES.ys as y, i}
		{@const tag = switchTag(i)}
		{@const pressed = on(tag)}
		<T.Mesh position={[SWITCHES.x, y, BOARD.z + 4.4]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[4.8, 4.8, 2.8, 6]} />
			<T.MeshStandardMaterial color="#9a9ea2" roughness={0.4} metalness={0.7} />
		</T.Mesh>
		<T.Mesh position={[SWITCHES.x, y, BOARD.z + 7]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[3.3, 3.6, 3, 20]} />
			<T.MeshStandardMaterial color="#b3b7bb" roughness={0.35} metalness={0.8} />
		</T.Mesh>
		<T.Mesh position={[SWITCHES.x, y, BOARD.z + 6]}>
			<T.TorusGeometry args={[4.6, 0.8, 8, 28]} />
			<T.MeshBasicMaterial color={pressed ? YELLOW : '#3a3d40'} toneMapped={false} />
		</T.Mesh>
		<T.Mesh position={[SWITCHES.x, y, BOARD.z + (pressed ? 9 : 11.5)]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[2.9, 2.9, pressed ? 2 : 7, 20]} />
			<T.MeshBasicMaterial color="#0b0b0d" toneMapped={false} />
		</T.Mesh>
		<Print w={15} h={7} position={[SWITCHES.x + 15, y + 1, BOARD.z + 3.12]} items={[{ text: `S${4 - i}`, x: 7.5, y: 3.6, size: 2.6, align: 'center', color: '#2a2d30', weight: 600 }]} plate={{ fill: '#f2f3f3', radius: 1 }} res={14} />
		{#if hovered === tag}
			<T.Mesh position={[SWITCHES.x + 7, y, BOARD.z + 3.2]}>
				<T.PlaneGeometry args={[36, 18]} />
				<T.MeshBasicMaterial color="#ffffff" transparent opacity={0.14} depthWrite={false} toneMapped={false} />
			</T.Mesh>
		{/if}
		<T.Mesh position={[SWITCHES.x + 7, y, BOARD.z + 20]} onpointerenter={() => onhover?.({ tag, text: () => (on(tag) ? 'on' : 'off') })} onpointerleave={() => onhover?.(null)}>
			<T.PlaneGeometry args={[36, 20]} />
			<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
		</T.Mesh>
	{/each}

	<!-- the slider: its printed scale, its slot, and the knob, moved by AI_00;
	     parked at 0 V and greyed while the input reports a fault instead of a reading -->
	<Print w={16} h={64} position={[SLIDER.x - 16, (SLIDER.y0 + SLIDER.y1) / 2 - 2, BOARD.z + 3.1]} items={scaleInk} paint={scalePaint} res={12} />
	<T.Mesh position={[SLIDER.x, (SLIDER.y0 + SLIDER.y1) / 2, BOARD.z + 3.2]}>
		<T.BoxGeometry args={[2, SLIDER.y1 - SLIDER.y0 + 14, 0.6]} />
		<T.MeshBasicMaterial color="#020203" toneMapped={false} />
	</T.Mesh>
	<T.Group position={[SLIDER.x, SLIDER.y0 + (slide ?? 0) * (SLIDER.y1 - SLIDER.y0), BOARD.z + 8]}>
		<T.Mesh>
			<RoundedBoxGeometry args={[12, 18, 10]} radius={1.4} />
			<T.MeshStandardMaterial color={slide === null ? '#4a4d50' : '#0c0d0e'} roughness={0.5} envMapIntensity={0.4} />
		</T.Mesh>
		{#each [-5, -2.5, 0, 2.5, 5] as dy}
			<T.Mesh position={[0, dy, 5.1]}>
				<T.PlaneGeometry args={[12, dy === 0 ? 0.9 : 0.5]} />
				<T.MeshBasicMaterial color={dy === 0 ? (slide === null ? '#8a8d90' : '#f2f3f3') : '#2c2f32'} toneMapped={false} />
			</T.Mesh>
		{/each}
	</T.Group>

	<!-- AXC F 2152: bus base and housing (vented down its left side); the lower-left corner of the face steps
	     back and the supply plug sits inside the step; the sockets are underneath -->
	{#if bodies.controller}
		<T.Mesh geometry={bodies.controller} scale={1000}>
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
	{:else}
		<T.Mesh position={[AXC.w / 2, 0, 6]}>
			<T.BoxGeometry args={[AXC.w, AXC.h, 16]} />
			<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
		</T.Mesh>
		<!-- the housing in three blocks: above the step, right of it, and the shallower block behind it -->
		<T.Mesh position={[AXC.w / 2, (AXC.h - 5) / 2 - (STEP.top - 2.5) / 2, (AXC.d + 10) / 2]}>
			<RoundedBoxGeometry args={[AXC.w - 0.6, STEP.top - 2.5, AXC.d - 10]} radius={1.6} />
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
		<T.Mesh position={[(STEP.w + AXC.w) / 2, AXC.h / 2 - (STEP.top + AXC.h - 2.5) / 2, (AXC.d + 10) / 2]}>
			<RoundedBoxGeometry args={[AXC.w - STEP.w - 0.3, AXC.h - 2.5 - STEP.top + 3, AXC.d - 10]} radius={1.6} />
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
		<T.Mesh position={[STEP.w / 2 + 0.6, AXC.h / 2 - (STEP.top + AXC.h - 2.5) / 2, (AXC.d - STEP.depth + 10) / 2]}>
			<T.BoxGeometry args={[STEP.w + 0.6, AXC.h - 2.5 - STEP.top + 3, AXC.d - STEP.depth - 10]} />
			<T.MeshStandardMaterial color={GREY_2} roughness={0.7} />
		</T.Mesh>
		<Print w={STEP.w - 1} h={22} position={[STEP.w / 2 + 0.4, AXC.h / 2 - STEP.top - 11.5, AXC.d - STEP.depth + 0.08]} items={[]} paint={stepPaint} res={10} />
		<Print w={AXC.d - 10} h={AXC.h - 12} position={[0.2, 0, (AXC.d + 10) / 2]} rotation={[0, -Math.PI / 2, 0]} items={[]} paint={axcSide} res={8} />
	{/if}
	<Print w={AXC.w} h={AXC.h} position={[AXC.w / 2, 0, AXC.d + 0.08]} items={axcInk} paint={axcPaint} res={14} />
	<T.Mesh position={[10.4, AXC.h / 2 - 110.5, AXC.d - STEP.depth + 6.5]}>
		<RoundedBoxGeometry args={[12.4, 23, 13]} radius={0.8} />
		<T.MeshStandardMaterial color="#1c1d1f" roughness={0.6} />
	</T.Mesh>
	<Print w={12} h={23} position={[10.4, AXC.h / 2 - 110.5, AXC.d - STEP.depth + 13.08]} items={[]} paint={plugPaint} res={12} />
	<!-- the Ethernet plug in the socket underneath, its link lamp lit while frames arrive -->
	<T.Mesh position={[31, -AXC.h / 2 - 1, 58]}>
		<T.BoxGeometry args={[11, 12, 13]} />
		<T.MeshStandardMaterial color="#1d4fb8" roughness={0.6} />
	</T.Mesh>
	<Led position={[24, -AXC.h / 2 + 2.8, AXC.d - 4.2]} size={[2, 1.6]} on={live} color="#9dff3c" off="#5d6265" />
	<T.Mesh position={[AXC.w / 2, -AXC.h / 2 - 1.5, 8]}>
		<T.BoxGeometry args={[7, 5, 9]} />
		<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
	</T.Mesh>
	{#each LAMPS as pair, i}
		{#each pair as name, j}
			<Led
				position={[LAMP.x + j * LAMP.dx, AXC.h / 2 - LAMP.y - i * LAMP.dy, AXC.d + 0.3]}
				size={[2.4, 1.8]}
				on={(name === 'RUN' && live) || (name === 'D' && online)}
				color={name === 'D' ? '#ffe24a' : '#9dff3c'}
				off="#6a6f72"
				spread={2.6}
			/>
		{/each}
	{/each}

	<!-- AXL F BP SE4: the backplane, with its own strip (marker, lamps, supply points) between the controller and the elements -->
	{#if bodies.backplane}
		<T.Mesh geometry={bodies.backplane} scale={1000}>
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
	{:else}
		<T.Mesh position={[BP.x + BP.w / 2, 0, BP.d / 2]}>
			<RoundedBoxGeometry args={[BP.w - 0.4, BP.h, BP.d]} radius={1} />
			<T.MeshStandardMaterial color={GREY} roughness={0.7} />
		</T.Mesh>
	{/if}
	<Print w={10} h={BP.h} position={[BP.x + 5, 0, BP.d + 0.08]} items={[{ text: 'AXL F BP SE4', x: 5.2, y: 42, size: 1.3, align: 'center', rotate: -90, color: INK, weight: 600, spacing: 0.1 }]} paint={bpPaint} res={12} />
	{#each BP_LAMPS as [x, y]}
		<Led position={[BP.x + x, BP.h / 2 - y, BP.d + 0.3]} size={[1.6, 2.2]} on={online} color="#7dff3c" off="#5d6265" spread={2.4} />
	{/each}
	<T.Mesh position={[BP.x + BP.w - 3, -BP.h / 2 - 1.5, 8]}>
		<T.BoxGeometry args={[6, 5, 9]} />
		<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
	</T.Mesh>

	{#each ELEMENTS as e (e.slot)}
		{@const x = seX(e)}
		{@const top = seTop(e)}
		<!-- one Smart Element: its housing, its face, its orange latch -->
		{#if bodies.element}
			<T.Mesh geometry={bodies.element} scale={1000} position={[x - seX(ELEMENTS[0]), top - seTop(ELEMENTS[0]), 0]}>
				<T.MeshStandardMaterial color={GREY} roughness={0.7} />
			</T.Mesh>
		{:else}
			<T.Mesh position={[x + SE.w / 2, top - SE.h / 2, (SE.z0 + SE.z1) / 2]}>
				<RoundedBoxGeometry args={[SE.w, SE.h, SE.z1 - SE.z0]} radius={0.7} />
				<T.MeshStandardMaterial color={e.kind === 'cover' ? '#989d9f' : GREY} roughness={0.7} />
			</T.Mesh>
		{/if}
		<Print
			w={SE.w}
			h={SE.h}
			position={[x + SE.w / 2, top - SE.h / 2, SE.z1 + 0.08]}
			items={faceInk(e)}
			paint={e.kind === 'cover' ? pipes : terminals(e.kind === 'ai' ? 8 : 16)}
			res={16}
		/>
		<T.Mesh position={[x + 1.4, top - 32.4, SE.z1 + 1.2]}>
			<RoundedBoxGeometry args={[3.4, 9, 3]} radius={0.5} />
			<T.MeshBasicMaterial color={ORANGE} toneMapped={false} />
		</T.Mesh>
		<T.Mesh position={[x + 1.4, top - 31.6, SE.z1 + 2.76]}>
			<T.PlaneGeometry args={[2.8, 6]} />
			<T.MeshBasicMaterial color="#f2f3f3" toneMapped={false} />
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
