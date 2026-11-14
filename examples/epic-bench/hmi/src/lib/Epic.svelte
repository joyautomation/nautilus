<script lang="ts" module>
	// The bench groov EPIC as it stands on the desk: a GRV-EPIC-PR1 and four
	// modules on a GRV-EPIC-CHS4 chassis, bolted to a clear acrylic demo
	// stand that leans back, with a small aluminium bar of two pushbuttons,
	// a knob and a connector beside it. Built from boxes and extrusions of
	// our own, in millimetres, at the dimensions of Opto 22's published CAD
	// (chassis 173 x 125.6 x 101; processor 86.5 x 126.6 x 131 with the
	// screen closed; modules 19.5 wide on a 21.4 pitch, 126.8 tall, 127 deep,
	// the cover reaching 28 below the body). Lettering, lamp positions, the
	// stand and the bar follow photographs of the bench unit.
	import type { Box3 } from './fit';

	/** World units per millimetre. */
	const S = 0.01;
	/** The acrylic plate the chassis is bolted to, mm. x = 0 is the chassis's left edge, y = 0 its floor, z = 0 its back. */
	const PLATE = { x0: -42, x1: 248, y0: -62, y1: 208, z0: -12, z1: -2 };
	/** How far the stand leans back, radians. */
	const TILT = 0.28;
	const CX = 88;
	const CY = 40;
	/** A box on the leaning stand, as the upright box that contains it, in world units. */
	const box = (x0: number, x1: number, y0: number, y1: number, z0: number, z1: number): Box3 => {
		const ys: number[] = [];
		const zs: number[] = [];
		for (const y of [y0, y1])
			for (const z of [z0, z1]) {
				const Y = y - PLATE.y0;
				const Z = z - PLATE.z0;
				ys.push(PLATE.y0 + Y * Math.cos(TILT) + Z * Math.sin(TILT));
				zs.push(PLATE.z0 - Y * Math.sin(TILT) + Z * Math.cos(TILT));
			}
		return {
			min: [(x0 - CX) * S, (Math.min(...ys) - CY) * S, Math.min(...zs) * S],
			max: [(x1 - CX) * S, (Math.max(...ys) - CY) * S, Math.max(...zs) * S]
		};
	};
	/** What the camera keeps in frame in a wide or square window: the whole stand. */
	export const BOUNDS: Box3[] = [box(PLATE.x0, PLATE.x1, PLATE.y0, PLATE.y1, PLATE.z0, PLATE.z1), box(0, 202, -26, 160, 0, 131)];
	/** And in a tall one: the processor, the modules and their tags. */
	export const BOUNDS_CORE: Box3[] = [box(0, 202, -26, 160, 60, 131)];

	export interface Channel {
		tag: string;
		/** What the tooltip says the value is, read when it is shown so it follows the tag. */
		text: () => string;
	}

	interface Module {
		slot: number;
		part: string;
		/** What is printed on the module's head. */
		mark: string;
		/** The status light's colour on the bench unit. */
		light: string;
		channels: number;
		kind: 'di' | 'do' | 'ai';
		prefix: string;
		unit?: string;
		digits?: number;
	}

	const GREEN = '#1fd6a2';
	const MODULES: Module[] = [
		{ slot: 0, part: 'GRV-IACDCTTL-24', mark: 'In TTL', light: GREEN, channels: 24, kind: 'di', prefix: 'EPIC_M0_DI' },
		{ slot: 1, part: 'GRV-ODCI-12', mark: 'Out DC i', light: GREEN, channels: 12, kind: 'do', prefix: 'EPIC_M1_DO' },
		{ slot: 2, part: 'GRV-IICTD-12', mark: 'ICTD', light: '#ffa02e', channels: 12, kind: 'ai', prefix: 'EPIC_M2_AI', unit: '°C', digits: 1 },
		{ slot: 3, part: 'GRV-IV-24', mark: 'In -/+ 10V', light: '#2aa5ff', channels: 24, kind: 'ai', prefix: 'EPIC_M3_AI', unit: 'V', digits: 2 }
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
	import { AdditiveBlending, CanvasTexture, CatmullRomCurve3, DoubleSide, ExtrudeGeometry, Path, Shape, TubeGeometry, Vector3 } from 'three';
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

	// Layout, mm.
	const CHS = { w: 173, h: 125.6, d: 101 };
	const PR1 = { x0: 0.8, w: 86.5, y0: 2, h: 126.6, body: 119, front: 131 };
	const MOD = { x0: 89.3, pitch: 21.4, w: 19.5, top: 130, h: 126.8, back: 2, body: 93, front: 129, reach: 154.8 };
	const left = (m: Module) => MOD.x0 + m.slot * MOD.pitch;
	const cx = (m: Module) => left(m) + MOD.w / 2;
	// The numbers run down the cover's left: 0 under the wave, the last above the paddle.
	const ROW0 = 43;
	const ROWS = 88;
	const pitch = (m: Module) => ROWS / (m.channels - 1);
	const rowY = (m: Module, ch: number) => MOD.top - ROW0 - ch * pitch(m);
	const NUM_X = 6.4;

	const BLACK = '#0b0c0d';
	const COVER = '#111214';
	const STEEL = '#8b9096';
	const ALU = '#aeb3b8';

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

	// A module's hinged cover, seen from the front: its top edge is a wave,
	// low on the left and high on the right, and its foot is a rounded paddle
	// below the body. The status light is the band along the wave.
	const WAVE = { lo: MOD.top - 35, hi: MOD.top - 25 };
	const HALF = MOD.w / 2 - 0.3;
	const FOOT = MOD.top - MOD.reach + HALF;
	const coverFace = (() => {
		const o = new Shape();
		o.moveTo(-HALF, FOOT);
		o.lineTo(-HALF, WAVE.lo);
		o.bezierCurveTo(-3, WAVE.lo + 4, 4, WAVE.lo + 5, HALF, WAVE.hi);
		o.lineTo(HALF, FOOT);
		o.absarc(0, FOOT, HALF, 0, -Math.PI, true);
		return new ExtrudeGeometry(o, { depth: 3.4, bevelEnabled: true, bevelSize: 0.6, bevelThickness: 0.6, bevelSegments: 3, curveSegments: 24 });
	})();
	const lens = (() => {
		const o = new Shape();
		o.moveTo(-HALF, WAVE.lo + 0.9);
		o.bezierCurveTo(-3, WAVE.lo + 4.9, 4, WAVE.lo + 5.9, HALF, WAVE.hi + 0.9);
		o.lineTo(HALF, WAVE.hi + 1.5);
		o.bezierCurveTo(4, WAVE.lo + 8.2, -3, WAVE.lo + 8.4, -HALF, WAVE.lo + 4.4);
		o.closePath();
		return new ExtrudeGeometry(o, { depth: 0.9, bevelEnabled: false, curveSegments: 24 });
	})();
	// Seen from the side (drawn with -z along the shape's x, then turned to
	// face along the chassis): the body with its full-depth head, and the
	// cover's side wall, a wedge that thins toward the paddle and leaves the
	// wiring space open under it.
	const bodySide = (() => {
		const o = new Shape();
		const b = MOD.top - MOD.h;
		o.moveTo(-MOD.back, b);
		o.lineTo(-MOD.back, MOD.top);
		o.lineTo(-(MOD.front - 2.5), MOD.top);
		o.quadraticCurveTo(-(MOD.front - 0.6), MOD.top, -(MOD.front - 0.6), MOD.top - 2.5);
		o.lineTo(-(MOD.front - 0.6), MOD.top - 38);
		o.lineTo(-(MOD.body + 6), MOD.top - 38);
		o.quadraticCurveTo(-MOD.body, MOD.top - 38, -MOD.body, MOD.top - 46);
		o.lineTo(-MOD.body, b);
		o.closePath();
		return new ExtrudeGeometry(o, { depth: MOD.w, bevelEnabled: false });
	})();
	const coverSide = (() => {
		const o = new Shape();
		o.moveTo(-(MOD.front - 3), MOD.top - 30);
		o.lineTo(-(MOD.body + 1), MOD.top - 30);
		o.quadraticCurveTo(-(MOD.body + 12), MOD.top - 88, -(MOD.front - 4), MOD.top - 112);
		o.lineTo(-(MOD.front - 3), MOD.top - 112);
		o.closePath();
		return new ExtrudeGeometry(o, { depth: 1.3, bevelEnabled: false });
	})();
	/** The soft light a lit lens throws on the faces round it. */
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

	/** A module's head: what it is, over the two linked rings (gloss on matte). */
	const headInk = (m: Module): Ink[] => [{ text: m.mark, x: MOD.w / 2, y: 3.2, size: m.mark.length > 8 ? 1.75 : 2.1, align: 'center', color: '#f2f3f4', weight: 650 }];
	const rings: Paint = (ctx) => {
		ctx.strokeStyle = '#030303';
		ctx.lineWidth = 1.8;
		for (const x of [MOD.w / 2 - 3.6, MOD.w / 2 + 3.6]) {
			ctx.beginPath();
			ctx.arc(x, 10.6, 3.1, 0, Math.PI * 2);
			ctx.stroke();
		}
	};
	/** The cover's face, from the module's top down: the raised rim round its field, and the part number low on the paddle. */
	const COVER_PRINT = { top: MOD.top - 14, h: 142 };
	const rim: Paint = (ctx) => {
		ctx.strokeStyle = '#2c2f33';
		ctx.lineWidth = 0.7;
		ctx.beginPath();
		ctx.moveTo(MOD.w - 2.2, 12);
		ctx.lineTo(MOD.w - 2.2, 118);
		ctx.quadraticCurveTo(MOD.w - 2.4, 134, 9, 136);
		ctx.quadraticCurveTo(2.4, 135, 2.2, 122);
		ctx.lineTo(2.2, 31);
		ctx.stroke();
	};
	function coverInk(m: Module): Ink[] {
		const items: Ink[] = [{ text: m.part, x: MOD.w - 5.2, y: 98, size: 1.9, align: 'center', rotate: -90, color: '#7c8288', weight: 600, spacing: 0.15 }];
		// Only the discrete modules carry channel numbers; the analog covers are blank.
		if (m.kind === 'ai') return items;
		const size = m.channels > 12 ? 2 : 2.4;
		for (let ch = 0; ch < m.channels; ch++)
			items.push({ text: String(ch), x: NUM_X, y: COVER_PRINT.top - rowY(m, ch), size, align: 'center', color: '#e9ebed', weight: 600 });
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
	const CALLOUT = { w: 54, h: 20, y: MOD.top + 20 };
	// Each tag sits over its own module's top, shifted so the two don't meet.
	const calloutX = (m: Module) => (m.slot === 2 ? cx(m) - CALLOUT.w / 2 + 3 : cx(m) + CALLOUT.w / 2 - 12);

	// Vent slots, as print: on a module's top (back of the module at the top
	// of the drawing), on its side (front at the left), on the processor's
	// top, and through the chassis floor.
	const slot = (ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number) => {
		ctx.beginPath();
		ctx.roundRect(x, y, w, h, 0.5);
		ctx.fill();
	};
	const moduleTop: Paint = (ctx) => {
		ctx.fillStyle = '#000000';
		for (let i = 0; i < 10; i++) slot(ctx, 3.6, 12 + i * 6.3, MOD.w - 7.2, 3.3);
	};
	const moduleSide: Paint = (ctx) => {
		ctx.fillStyle = '#000000';
		for (const x of [28, 60]) for (let i = 0; i < 9; i++) slot(ctx, x, 5 + i * 13.4, 3.4, 10.6);
		ctx.beginPath();
		ctx.roundRect(8, 9, 9, 2.2, 1.1);
		ctx.fill();
	};
	const processorTop: Paint = (ctx) => {
		ctx.fillStyle = '#000000';
		for (const [x0, cols] of [[4, 3], [45, 3]] as const)
			for (let c = 0; c < cols; c++)
				for (let r = 0; r < 12; r++) slot(ctx, x0 + c * 12.6, 10 + r * 7.6 + (r > 5 ? 9 : 0), 10.4, 4.2);
	};
	const floor = (() => {
		const c = document.createElement('canvas');
		c.width = 692;
		c.height = 404;
		const ctx = c.getContext('2d')!;
		ctx.scale(4, 4);
		ctx.fillStyle = STEEL;
		ctx.fillRect(0, 0, CHS.w, CHS.d);
		ctx.fillStyle = '#0a0b0c';
		for (let col = 0; col < 6; col++) for (let r = 0; r < 6; r++) slot(ctx, 4 + col * 14, 32 + r * 6.4, 11, 3.6);
		for (let col = 0; col < 4; col++) for (let r = 0; r < 10; r++) slot(ctx, MOD.x0 + 4 + col * MOD.pitch, 6 + r * 6.4, 12, 3.6);
		const t = new CanvasTexture(c);
		t.anisotropy = 8;
		return t;
	})();

	// The touchscreen's housing: a bevelled frame round a sunken display, and
	// under it a skirt whose edge sweeps from high on the left down to the
	// right. The display shows the sign-in page, as plain blocks.
	const DISPLAY = { x0: PR1.x0 + 8.5, x1: PR1.x0 + 74.5, y0: PR1.y0 + 9.6, y1: PR1.y0 + 118.6 };
	const bezel = (() => {
		const x1 = PR1.x0 + PR1.w;
		const y1 = PR1.y0 + PR1.h;
		const o = new Shape();
		o.moveTo(PR1.x0 + 3, PR1.y0);
		o.lineTo(x1 - 3, PR1.y0);
		o.quadraticCurveTo(x1, PR1.y0, x1, PR1.y0 + 3);
		o.lineTo(x1, y1 - 3);
		o.quadraticCurveTo(x1, y1, x1 - 3, y1);
		o.lineTo(PR1.x0 + 3, y1);
		o.quadraticCurveTo(PR1.x0, y1, PR1.x0, y1 - 3);
		o.lineTo(PR1.x0, PR1.y0 + 3);
		o.quadraticCurveTo(PR1.x0, PR1.y0, PR1.x0 + 3, PR1.y0);
		const hole = new Path();
		hole.moveTo(DISPLAY.x0, DISPLAY.y0);
		hole.lineTo(DISPLAY.x1, DISPLAY.y0);
		hole.lineTo(DISPLAY.x1, DISPLAY.y1);
		hole.lineTo(DISPLAY.x0, DISPLAY.y1);
		hole.closePath();
		o.holes.push(hole);
		return new ExtrudeGeometry(o, { depth: 8.4, bevelEnabled: true, bevelSize: 1.8, bevelThickness: 1.8, bevelSegments: 3, curveSegments: 12 });
	})();
	const skirt = (() => {
		const x1 = PR1.x0 + PR1.w;
		const o = new Shape();
		o.moveTo(PR1.x0, PR1.y0 + 4);
		o.lineTo(x1, PR1.y0 + 4);
		o.lineTo(x1, PR1.y0 - 16);
		o.quadraticCurveTo(x1, PR1.y0 - 26, x1 - 12, PR1.y0 - 26);
		o.lineTo(PR1.x0 + 50, PR1.y0 - 26);
		o.bezierCurveTo(PR1.x0 + 26, PR1.y0 - 26, PR1.x0 + 14, PR1.y0 - 12, PR1.x0, PR1.y0 - 5);
		o.closePath();
		return new ExtrudeGeometry(o, { depth: 2.6, bevelEnabled: true, bevelSize: 0.8, bevelThickness: 0.8, bevelSegments: 2, curveSegments: 24 });
	})();
	const DW = DISPLAY.x1 - DISPLAY.x0;
	const DH = DISPLAY.y1 - DISPLAY.y0;
	const signIn: Paint = (ctx) => {
		const g = ctx.createLinearGradient(0, 0, DW, DH);
		g.addColorStop(0, '#191b2c');
		g.addColorStop(1, '#0b0c12');
		ctx.fillStyle = g;
		ctx.fillRect(0, 0, DW, DH);
		ctx.fillStyle = '#3b3f66';
		for (const y of [35, 54]) {
			ctx.beginPath();
			ctx.roundRect(6, y, DW - 12, 9, 1.2);
			ctx.fill();
		}
		ctx.strokeStyle = '#c9cbd6';
		ctx.lineWidth = 0.4;
		ctx.strokeRect(6.4, 68.4, 2.6, 2.6);
		ctx.fillStyle = '#1b74f0';
		ctx.beginPath();
		ctx.roundRect(6, 78, DW - 12, 9.5, 1.4);
		ctx.fill();
	};
	const signInInk: Ink[] = [
		{ text: 'groov EPIC', x: DW / 2, y: 17, size: 6.2, align: 'center', color: '#e8e9f0', weight: 800, italic: true },
		{ text: 'Username', x: 6, y: 32, size: 1.9, color: '#d5d7e2', weight: 600 },
		{ text: 'Password', x: 6, y: 51, size: 1.9, color: '#d5d7e2', weight: 600 },
		{ text: 'Remember me', x: 11, y: 69.8, size: 1.9, color: '#d5d7e2', weight: 500 },
		{ text: 'Sign In', x: DW / 2, y: 82.8, size: 2.2, align: 'center', color: '#ffffff', weight: 650 }
	];

	// The stand, in the leaning frame: where the desk is under a point, and
	// the cables that run down to it.
	const deskY = (z: number) => PLATE.y0 - (z - PLATE.z0) * Math.tan(TILT);
	const onDesk = (x: number, z: number): [number, number, number] => [x, deskY(z) + 3, z];
	const cable = (pts: [number, number, number][], r: number) => new TubeGeometry(new CatmullRomCurve3(pts.map((p) => new Vector3(...p))), 48, r, 8);
	const BAR = { x: 222, y0: 8, y1: 134, parts: [112, 84, 54, 24] };
	const CABLES = [
		// Ethernet, from under the processor
		{ color: '#1d4fb8', g: cable([[34, 4, 86], [33, -30, 98], [26, -80, 128], onDesk(-10, 175), onDesk(-110, 215), onDesk(-230, 235)], 2.4) },
		// the grey cable from the bar's connector
		{ color: '#878b90', g: cable([[BAR.x, 24, 22], [BAR.x + 1, 16, 44], [BAR.x - 2, -40, 70], onDesk(BAR.x - 20, 120), onDesk(150, 210), onDesk(20, 300)], 1.9) }
	];
	/** The bar's wiring: a flat run of coloured wires from behind the modules. */
	const RIBBON = ['#c9302a', '#e8822a', '#e8c62a', '#2a5fc4', '#7d3fb5'].map((color, i) => ({
		color,
		g: cable([[CHS.w - 6, 12 + i * 1.6, 6], [CHS.w + 12, 14 + i * 1.6, 2], [BAR.x - 22, 20 + i * 1.6, 2], [BAR.x - 14, 22 + i * 1.6, 4]], 0.7)
	}));
</script>

<T.Group scale={S} position={[-CX * S, -CY * S, 0]}>
	<!-- the desk -->
	<T.Mesh position={[CX, PLATE.y0 - 5, 70]}>
		<RoundedBoxGeometry args={[660, 10, 520]} radius={2} />
		<T.MeshStandardMaterial color="#b6935c" roughness={0.75} metalness={0} />
	</T.Mesh>

	<T.Group position={[0, PLATE.y0, PLATE.z0]} rotation={[-TILT, 0, 0]}>
	<T.Group position={[0, -PLATE.y0, -PLATE.z0]}>
	<!-- the stand: a clear acrylic plate, and the clear leg behind that holds it leaning -->
	<T.Mesh position={[(PLATE.x0 + PLATE.x1) / 2, (PLATE.y0 + PLATE.y1) / 2, (PLATE.z0 + PLATE.z1) / 2]} renderOrder={-1}>
		<RoundedBoxGeometry args={[PLATE.x1 - PLATE.x0, PLATE.y1 - PLATE.y0, PLATE.z1 - PLATE.z0]} radius={1} />
		<T.MeshStandardMaterial color="#e9f4f6" transparent opacity={0.2} roughness={0.08} metalness={0} depthWrite={false} />
	</T.Mesh>
	<!-- its cut edges catch the light -->
	{#each [[(PLATE.x0 + PLATE.x1) / 2, PLATE.y1, PLATE.x1 - PLATE.x0, 0.8], [(PLATE.x0 + PLATE.x1) / 2, PLATE.y0, PLATE.x1 - PLATE.x0, 0.8], [PLATE.x0, (PLATE.y0 + PLATE.y1) / 2, 0.8, PLATE.y1 - PLATE.y0], [PLATE.x1, (PLATE.y0 + PLATE.y1) / 2, 0.8, PLATE.y1 - PLATE.y0]] as [x, y, w, h]}
		<T.Mesh position={[x, y, (PLATE.z0 + PLATE.z1) / 2]} renderOrder={-1}>
			<T.BoxGeometry args={[w, h, PLATE.z1 - PLATE.z0]} />
			<T.MeshBasicMaterial color="#f4fbfc" transparent opacity={0.5} depthWrite={false} toneMapped={false} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[103, 34, PLATE.z0 - 57]} rotation={[0.6, 0, 0]} renderOrder={-2}>
		<T.BoxGeometry args={[170, 178, 8]} />
		<T.MeshStandardMaterial color="#e9f4f6" transparent opacity={0.16} roughness={0.08} depthWrite={false} />
	</T.Mesh>
	{#each [40, 166] as x}
		<T.Mesh position={[x, 112, PLATE.z0 - 5]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[7, 7, 10, 20]} />
			<T.MeshStandardMaterial color={ALU} roughness={0.4} metalness={0.7} />
		</T.Mesh>
	{/each}

	<!-- GRV-EPIC-CHS4: the steel back, and the slotted floor the processor and modules stand on -->
	<T.Mesh position={[CHS.w / 2, CHS.h / 2 + 3, -0.2]}>
		<T.BoxGeometry args={[CHS.w, CHS.h + 6, 3.2]} />
		<T.MeshStandardMaterial color={STEEL} roughness={0.5} metalness={0.6} />
	</T.Mesh>
	<T.Mesh position={[CHS.w / 2, -0.8, CHS.d / 2]} rotation={[-Math.PI / 2, 0, 0]}>
		<T.BoxGeometry args={[CHS.w, CHS.d, 1.6]} />
		<T.MeshStandardMaterial map={floor} roughness={0.45} metalness={0.6} />
	</T.Mesh>

	<!-- GRV-EPIC-PR1: the processor (with its power supply) as one block, vented on top -->
	<T.Mesh position={[PR1.x0 + PR1.w / 2, PR1.y0 + PR1.h / 2, (PR1.body + 1.6) / 2]}>
		<RoundedBoxGeometry args={[PR1.w, PR1.h, PR1.body - 1.6]} radius={1.6} />
		<T.MeshStandardMaterial color={BLACK} roughness={0.6} metalness={0} envMapIntensity={0.3} />
	</T.Mesh>
	<Print w={PR1.w} h={PR1.body - 4} position={[PR1.x0 + PR1.w / 2, PR1.y0 + PR1.h + 0.08, PR1.body / 2 + 1]} rotation={[-Math.PI / 2, 0, 0]} items={[]} paint={processorTop} res={8} />
	<!-- its touchscreen, closed over it and lit: the sign-in page -->
	<T.Mesh geometry={bezel} position={[0, 0, PR1.body + 1.8]}>
		<T.MeshStandardMaterial color={COVER} roughness={0.5} metalness={0} envMapIntensity={0.35} />
	</T.Mesh>
	<Print w={DW} h={DH} position={[(DISPLAY.x0 + DISPLAY.x1) / 2, (DISPLAY.y0 + DISPLAY.y1) / 2, PR1.body + 6]} items={signInInk} paint={signIn} res={12} />
	<T.Mesh position={[(DISPLAY.x0 + DISPLAY.x1) / 2, (DISPLAY.y0 + DISPLAY.y1) / 2, PR1.body + 5.6]}>
		<T.BoxGeometry args={[DW + 4, DH + 4, 0.6]} />
		<T.MeshStandardMaterial color="#050506" roughness={0.3} />
	</T.Mesh>
	<T.Mesh geometry={skirt} position={[0, 0, PR1.front - 4.4]}>
		<T.MeshStandardMaterial color={COVER} roughness={0.5} metalness={0} envMapIntensity={0.35} />
	</T.Mesh>
	<Print
		w={56}
		h={20}
		position={[PR1.x0 + 53, PR1.y0 - 11.5, PR1.front - 0.9]}
		items={[
			{ text: 'groov', x: 28, y: 7.2, size: 8.6, align: 'center', italic: true, weight: 800, color: '#060607' },
			{ text: 'GRV-EPIC-PR1', x: 28, y: 16.6, size: 2, align: 'center', weight: 600, color: '#6f757b', spacing: 0.4 }
		]}
	/>

	{#each MODULES as m (m.slot)}
		<!-- one module: the body and its head, the terminals behind the cover, and the cover with its two side walls -->
		<T.Mesh geometry={bodySide} position={[left(m), 0, 0]} rotation={[0, Math.PI / 2, 0]}>
			<T.MeshStandardMaterial color={BLACK} roughness={0.6} metalness={0} envMapIntensity={0.3} />
		</T.Mesh>
		<T.Mesh position={[cx(m), MOD.top - 80, MOD.body + 6]}>
			<T.BoxGeometry args={[MOD.w - 5, 84, 12]} />
			<T.MeshStandardMaterial color="#060607" roughness={0.7} />
		</T.Mesh>
		{#each [left(m) + 0.2, left(m) + MOD.w - 1.5] as x}
			<T.Mesh geometry={coverSide} position={[x, 0, 0]} rotation={[0, Math.PI / 2, 0]}>
				<T.MeshStandardMaterial color={COVER} roughness={0.5} metalness={0} envMapIntensity={0.35} side={DoubleSide} />
			</T.Mesh>
		{/each}
		<T.Mesh geometry={coverFace} position={[cx(m), 0, MOD.front - 4]}>
			<T.MeshStandardMaterial color={COVER} roughness={0.45} metalness={0} envMapIntensity={0.35} />
		</T.Mesh>
		<!-- the vents and the latch on top; on the last module, the vents down its open side -->
		<Print w={MOD.w} h={MOD.body - MOD.back} position={[cx(m), MOD.top + 0.08, (MOD.body + MOD.back) / 2]} rotation={[-Math.PI / 2, 0, 0]} items={[]} paint={moduleTop} res={10} />
		<T.Mesh position={[cx(m), MOD.top + 0.9, MOD.body + 9]}>
			<RoundedBoxGeometry args={[9, 2.4, 7]} radius={0.6} />
			<T.MeshStandardMaterial color="#3b3f44" roughness={0.5} />
		</T.Mesh>
		{#if m.slot === MODULES.length - 1}
			<Print w={MOD.body - MOD.back} h={MOD.h} position={[left(m) + MOD.w + 0.08, MOD.top - MOD.h / 2, (MOD.body + MOD.back) / 2]} rotation={[0, Math.PI / 2, 0]} items={[]} paint={moduleSide} res={8} />
		{/if}

		<!-- the head's lettering, the status light along the wave (dark when the driver is offline), the cover's print -->
		<Print w={MOD.w} h={16} position={[cx(m), MOD.top - 8.4, MOD.front - 0.5]} items={headInk(m)} paint={rings} res={16} />
		<T.Mesh geometry={lens} position={[cx(m), 0, MOD.front - 0.55]}>
			<T.MeshBasicMaterial color={online ? m.light : '#15191c'} toneMapped={false} />
		</T.Mesh>
		{#if online}
			<T.Mesh position={[cx(m) - 3, MOD.top - 32, MOD.front + 0.45]} renderOrder={3}>
				<T.PlaneGeometry args={[24, 20]} />
				<T.MeshBasicMaterial map={haze} color={m.light} transparent opacity={0.42} blending={AdditiveBlending} depthWrite={false} toneMapped={false} />
			</T.Mesh>
		{/if}
		<Print w={MOD.w} h={COVER_PRINT.h} position={[cx(m), COVER_PRINT.top - COVER_PRINT.h / 2, MOD.front + 0.08]} items={coverInk(m)} paint={rim} res={14} />

		{#each { length: m.channels } as _, ch (ch)}
			{@const tag = tagOf(m, ch)}
			{@const y = rowY(m, ch)}
			<!-- a discrete channel's lamp is its number: lit, the number is a white glow.
			     The analog covers are blank; their rows only answer the pointer. -->
			{#if m.kind !== 'ai' && on(tag)}
				<Led position={[left(m) + NUM_X, y, MOD.front + 0.3]} size={[m.channels > 12 ? 3.4 : 3.8, Math.min(2.8, pitch(m) * 0.72)]} on color="#ffffff" spread={3.2} />
			{/if}
			{#if hovered === tag}
				<T.Mesh position={[cx(m), y, MOD.front + 0.15]}>
					<T.PlaneGeometry args={[MOD.w - 2, Math.max(pitch(m), 3)]} />
					<T.MeshBasicMaterial color="#ffffff" transparent opacity={0.16} depthWrite={false} toneMapped={false} />
				</T.Mesh>
			{/if}
			<T.Mesh position={[cx(m), y, MOD.front + 0.7]} onpointerenter={() => onhover?.(describe(m, ch))} onpointerleave={() => onhover?.(null)}>
				<T.PlaneGeometry args={[MOD.w, pitch(m)]} />
				<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
			</T.Mesh>
		{/each}

		{#if m.kind === 'ai'}
			<T.Mesh position={[cx(m), (MOD.top + CALLOUT.y - CALLOUT.h / 2) / 2, MOD.front - 4]}>
				<T.BoxGeometry args={[0.5, CALLOUT.y - CALLOUT.h / 2 - MOD.top, 0.5]} />
				<T.MeshBasicMaterial color="#d8d6d0" toneMapped={false} />
			</T.Mesh>
			<Print w={CALLOUT.w} h={CALLOUT.h} position={[calloutX(m), CALLOUT.y, MOD.front - 4]} items={calloutInk(m)} plate={{ fill: 'rgba(22,22,21,0.94)', stroke: '#55554f', radius: 2 }} res={14} />
		{/if}
	{/each}

	<!-- the bar beside the chassis: two pushbuttons, a knob and a connector on an
	     aluminium plate. Not bound to any tag: what it is wired to is not known here. -->
	<T.Mesh position={[BAR.x, (BAR.y0 + BAR.y1) / 2, 6]}>
		<RoundedBoxGeometry args={[26, BAR.y1 - BAR.y0 - 8, 16]} radius={2} />
		<T.MeshStandardMaterial color="#8d9297" roughness={0.45} metalness={0.6} />
	</T.Mesh>
	<T.Mesh position={[BAR.x, (BAR.y0 + BAR.y1) / 2, 15.5]}>
		<RoundedBoxGeometry args={[32, BAR.y1 - BAR.y0, 3]} radius={1} />
		<T.MeshStandardMaterial color={ALU} roughness={0.35} metalness={0.75} />
	</T.Mesh>
	{#each BAR.parts.slice(0, 2) as y}
		<T.Mesh position={[BAR.x, y, 17.6]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[8, 8.6, 1.6, 28]} />
			<T.MeshStandardMaterial color="#c3c7cb" roughness={0.25} metalness={0.9} />
		</T.Mesh>
		<T.Mesh position={[BAR.x, y, 18.4]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[5, 5, 1.6, 28]} />
			<T.MeshStandardMaterial color="#9ea3a8" roughness={0.3} metalness={0.9} />
		</T.Mesh>
	{/each}
	<T.Mesh position={[BAR.x, BAR.parts[2], 23]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[8, 8.4, 12, 28]} />
		<T.MeshStandardMaterial color="#b9bdc1" roughness={0.3} metalness={0.85} />
	</T.Mesh>
	<T.Mesh position={[BAR.x + 3.6, BAR.parts[2] - 3.6, 29.1]} rotation={[0, 0, Math.PI / 4]}>
		<T.PlaneGeometry args={[0.9, 6]} />
		<T.MeshBasicMaterial color="#1a1b1c" toneMapped={false} />
	</T.Mesh>
	<T.Mesh position={[BAR.x, BAR.parts[3], 22]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[6.4, 7.4, 12, 12]} />
		<T.MeshStandardMaterial color="#c3c7cb" roughness={0.25} metalness={0.9} />
	</T.Mesh>

	<!-- the cables -->
	{#each [...CABLES, ...RIBBON] as c}
		<T.Mesh geometry={c.g}>
			<T.MeshStandardMaterial color={c.color} roughness={0.6} />
		</T.Mesh>
	{/each}
	</T.Group>
	</T.Group>
</T.Group>
