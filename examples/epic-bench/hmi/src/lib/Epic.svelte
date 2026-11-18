<script lang="ts" module>
	// The bench groov EPIC as it stands on the desk: a GRV-EPIC-PR1 with its
	// power supply and four modules on a GRV-EPIC-CHS4 chassis, bolted to a
	// clear acrylic demo stand that leans back, with a small aluminium bar of
	// two pushbuttons, a knob and a connector beside it. The bodies are the
	// models in static/models (simplified from Opto 22's published CAD: see
	// the README there); everything live is drawn here, over them: the lit
	// screen, each module's lettering, status light and channel lamps, the
	// analog tags. The stand, the bar and the cables are ours, from
	// photographs of the bench unit.
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
	export const BOUNDS: Box3[] = [box(PLATE.x0, PLATE.x1, PLATE.y0, PLATE.y1, PLATE.z0, PLATE.z1), box(0, 202, -25, 165, 0, 142)];
	/** And in a tall one: the processor, the modules and their tags. */
	export const BOUNDS_CORE: Box3[] = [box(0, 202, -25, 165, 60, 142)];

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
	import { T, useThrelte } from '@threlte/core';
	import { RoundedBoxGeometry } from '@threlte/extras';
	import { getContext, onMount } from 'svelte';
	import { base } from '$app/paths';
	import { AdditiveBlending, CanvasTexture, CatmullRomCurve3, Mesh, MeshStandardMaterial, Plane, Raycaster, TubeGeometry, Vector2, Vector3, type BufferGeometry } from 'three';
	import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js';
	import { SCENE, type SceneContext } from '@joyautomation/nautilus-hmi-3d';
	import Led from './Led.svelte';
	import Print from './Print.svelte';
	import type { Ink, Paint } from './print';
	import { BAR_WIRING, knobAngle, knobValueAt, onPress, onRelease } from './bar';

	let {
		hovered = null,
		onhover,
		write
	}: {
		/** The channel under the pointer, to mark its row. */
		hovered?: string | null;
		onhover?: (ch: Channel | null) => void;
		/** Given, the bar's buttons press and its knob turns: the
		 *  simulated build (sim.yaml), whose inputs are tags. On the
		 *  controller they are the modules', so the page leaves this out
		 *  and the bar stays as drawn. Wiring: ./bar.ts (provisional). */
		write?: (tag: string, value: boolean | number) => void;
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

	// The bodies. Each file is one mesh in this component's own frame, in
	// metres: x from the chassis's left edge, y up from its floor, z out from
	// its back. module.glb is the module in slot 0, and module-light.glb the
	// light pipe along its cover's wave; the other three slots are the same
	// geometry moved along by the slot pitch.
	const PARTS = ['chassis', 'processor', 'power-supply', 'module', 'module-light'] as const;
	let bodies = $state<Partial<Record<(typeof PARTS)[number], BufferGeometry>>>({});
	onMount(() => {
		const loader = new GLTFLoader();
		for (const name of PARTS)
			loader.load(`${base}/models/${name}.glb`, (gltf) => {
				gltf.scene.traverse((o) => {
					if (o instanceof Mesh) bodies[name] = o.geometry;
				});
			});
	});
	// Satin black plastic, and the chassis's plain steel.
	const plastic = new MeshStandardMaterial({ color: '#0d0e0f', roughness: 0.55, metalness: 0, envMapIntensity: 0.45 });
	const steel = new MeshStandardMaterial({ color: '#8b9096', roughness: 0.5, metalness: 0.6 });

	// Where our own parts sit on those bodies, mm, read off the models.
	const PR1 = { x0: 0.8, w: 87.5, top: 128.8, front: 135.9 };
	const MOD = { x0: 90, pitch: 21.35, w: 19.6, top: 133, head: 141.8, field: 138.5, reach: -21.8 };
	const left = (m: Module) => MOD.x0 + m.slot * MOD.pitch;
	const cx = (m: Module) => left(m) + MOD.w / 2;
	// The numbers run down the cover's left: 0 under the wave, the last above the paddle.
	const ROW_TOP = 86;
	const ROWS = 76;
	const pitch = (m: Module) => ROWS / (m.channels - 1);
	const rowY = (m: Module, ch: number) => ROW_TOP - ch * pitch(m);
	const NUM_X = 8.4;

	const COVER = '#111214';
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

	/** The soft light a lit status light throws on the faces round it. */
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

	/** A module's head: what it is, printed above the two rings moulded into it. */
	const headInk = (m: Module): Ink[] => [{ text: m.mark, x: MOD.w / 2, y: 2.4, size: m.mark.length > 8 ? 1.7 : 2.1, align: 'center', color: '#f2f3f4', weight: 650 }];
	/** The cover's field, from y = 95 down: the channel numbers, and the part number low on the right. */
	const COVER_PRINT = { top: 95, h: 100 };
	function coverInk(m: Module): Ink[] {
		const items: Ink[] = [{ text: m.part, x: MOD.w - 5, y: 62, size: 1.9, align: 'center', rotate: -90, color: '#7c8288', weight: 600, spacing: 0.15 }];
		// Only the discrete modules carry channel numbers; the analog covers are blank.
		if (m.kind === 'ai') return items;
		const size = m.channels > 12 ? 1.9 : 2.4;
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

	// The touchscreen's display, sunk in the model's bezel: it shows the sign-in page, as plain blocks.
	const DISPLAY = { x0: 9.2, x1: 75.3, y0: 10.55, y1: 120.05, z: 133.9 };
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
		{ color: '#1d4fb8', g: cable([[34, 2, 90], [33, -30, 98], [26, -80, 128], onDesk(-10, 175), onDesk(-110, 215), onDesk(-230, 235)], 2.4) },
		// the grey cable from the bar's connector
		{ color: '#878b90', g: cable([[BAR.x, 24, 22], [BAR.x + 1, 16, 44], [BAR.x - 2, -40, 70], onDesk(BAR.x - 20, 120), onDesk(150, 210), onDesk(20, 300)], 1.9) }
	];
	/** The bar's wiring: a flat run of coloured wires from behind the modules. */
	const RIBBON = ['#c9302a', '#e8822a', '#e8c62a', '#2a5fc4', '#7d3fb5'].map((color, i) => ({
		color,
		g: cable([[167, 12 + i * 1.6, 6], [185, 14 + i * 1.6, 2], [BAR.x - 22, 20 + i * 1.6, 2], [BAR.x - 14, 22 + i * 1.6, 4]], 0.7)
	}));

	// Pressing and turning, on the simulated build only (`write` given), as
	// plcnext-bench does (#748). The view's OrbitControls would take a drag
	// on the knob as an orbit, so a press is caught on the window before the
	// canvas sees it: on a button or the knob the bar has it and the camera
	// never does; anywhere else it goes on to the controls. Our own raycast,
	// not the hover state, so a touch (no hover first) works too.
	const KNOB = BAR_WIRING.knob;
	/** The knob drawn as on the desk, before the bar was bound: pointing to half past four. */
	const KNOB_AS_DRAWN = (3 * Math.PI) / 4;
	const { camera, renderer } = useThrelte();
	/** The momentary button being held, if any. */
	let held = $state<number | null>(null);
	let turning = $state<number | null>(null);
	let buttonHits = $state<(Mesh | undefined)[]>([]);
	let knobHit = $state<Mesh>();
	let knobAt = $derived.by(() => {
		if (!write) return KNOB_AS_DRAWN;
		if (turning !== null) return knobAngle(turning);
		const v = num(KNOB.tag);
		return v === null ? KNOB_AS_DRAWN : knobAngle(v);
	});
	/** In while its input is on (or while held, before the frame shows it). */
	const pressed = (i: number) => !!write && (held === i || on(BAR_WIRING.buttons[i].tag));
	/** Its ring: lit by the tag it is wired to (its own input, or an output). */
	const ringLit = (i: number) => !!write && on(BAR_WIRING.buttons[i].ring.tag);
	const ray = new Raycaster();
	const ndc = new Vector2();
	function aim(e: PointerEvent) {
		const r = renderer.domElement.getBoundingClientRect();
		ndc.set(((e.clientX - r.left) / r.width) * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
		ray.setFromCamera(ndc, camera.current);
	}
	/** The knob's value with the pointer where it is, from the one it had. */
	function knobFrom(e: PointerEvent, prev: number): number | null {
		const m = knobHit;
		if (!m?.parent) return null;
		aim(e);
		m.updateWorldMatrix(true, false);
		const normal = new Vector3(0, 0, 1).transformDirection(m.matrixWorld);
		const at = new Vector3().setFromMatrixPosition(m.matrixWorld);
		const hit = ray.ray.intersectPlane(new Plane().setFromNormalAndCoplanarPoint(normal, at), new Vector3());
		if (!hit) return null;
		const p = m.parent.worldToLocal(hit);
		return knobValueAt(p.x - BAR.x, p.y - BAR.parts[2], prev);
	}
	// At most one write per 60 ms while turning, and only a changed value.
	let sent: number | null = null;
	let pending: number | null = null;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let settle: ReturnType<typeof setTimeout> | undefined;
	function flush() {
		timer = undefined;
		if (pending === null) return;
		write?.(KNOB.tag, pending);
		sent = pending;
		pending = null;
		timer = setTimeout(flush, 60);
	}
	function turnTo(v: number) {
		turning = v;
		if (v === sent && pending === null) return;
		pending = v;
		if (!timer) flush();
	}
	$effect(() => {
		if (!write) return;
		const canvas = renderer.domElement;
		const down = (e: PointerEvent) => {
			if (e.target !== canvas || e.button !== 0) return;
			aim(e);
			const targets = [...buttonHits, knobHit].filter((m): m is Mesh => !!m);
			const hit = ray.intersectObjects(targets, false)[0];
			if (!hit) return;
			e.stopPropagation();
			e.preventDefault();
			clearTimeout(settle);
			settle = undefined;
			if (hit.object === knobHit) {
				const v = knobFrom(e, num(KNOB.tag) ?? KNOB.min);
				if (v !== null) turnTo(v);
				else turning = num(KNOB.tag) ?? KNOB.min;
			} else {
				const i = buttonHits.indexOf(hit.object as Mesh);
				const b = BAR_WIRING.buttons[i];
				if (b) {
					write(b.tag, onPress(b.kind, raw(b.tag)));
					if (onRelease(b.kind) !== null) held = i;
				}
			}
		};
		const move = (e: PointerEvent) => {
			if (turning === null || settle !== undefined) return;
			const v = knobFrom(e, turning);
			if (v !== null) turnTo(v);
		};
		const up = () => {
			if (held !== null) {
				const b = BAR_WIRING.buttons[held];
				const v = onRelease(b.kind);
				if (v !== null) write(b.tag, v);
				held = null;
			}
			if (turning !== null && settle === undefined) {
				clearTimeout(timer);
				flush();
				// Hold the pointer where it was let go until the tag has caught up.
				settle = setTimeout(() => {
					turning = null;
					settle = undefined;
				}, 400);
			}
		};
		window.addEventListener('pointerdown', down, true);
		window.addEventListener('pointermove', move, true);
		window.addEventListener('pointerup', up, true);
		window.addEventListener('pointercancel', up, true);
		return () => {
			window.removeEventListener('pointerdown', down, true);
			window.removeEventListener('pointermove', move, true);
			window.removeEventListener('pointerup', up, true);
			window.removeEventListener('pointercancel', up, true);
		};
	});
	const cursor = (c: string) => write && (renderer.domElement.style.cursor = c);
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

	<!-- the bodies, from static/models (metres): chassis, processor, power supply, and one module per slot -->
	<T.Group scale={1000}>
		{#if bodies.chassis}<T.Mesh geometry={bodies.chassis} material={steel} />{/if}
		{#if bodies.processor}<T.Mesh geometry={bodies.processor} material={plastic} />{/if}
		{#if bodies['power-supply']}<T.Mesh geometry={bodies['power-supply']} material={plastic} />{/if}
		{#if bodies.module}
			{#each MODULES as m (m.slot)}
				<T.Mesh geometry={bodies.module} material={plastic} position={[(m.slot * MOD.pitch) / 1000, 0, 0]} />
			{/each}
		{/if}
		<!-- each module's status light, the CAD's own light pipe: its colour, dark when the driver is offline -->
		{#if bodies['module-light']}
			{#each MODULES as m (m.slot)}
				<T.Mesh geometry={bodies['module-light']} position={[(m.slot * MOD.pitch) / 1000, 0, 0]}>
					<T.MeshBasicMaterial color={online ? m.light : '#15191c'} toneMapped={false} />
				</T.Mesh>
			{/each}
		{/if}
	</T.Group>

	<!-- the touchscreen, lit: the sign-in page, behind the bezel -->
	<Print w={DW + 3} h={DH + 3} position={[(DISPLAY.x0 + DISPLAY.x1) / 2, (DISPLAY.y0 + DISPLAY.y1) / 2, DISPLAY.z]} items={signInInk} paint={signIn} res={12} />
	{#each MODULES as m (m.slot)}
		<!-- on each module: the head's lettering, the status light's haze, the cover's print -->
		<Print w={MOD.w} h={5} position={[cx(m), MOD.top - 4.4, MOD.head + 0.12]} items={headInk(m)} res={16} />
		{#if online}
			<T.Mesh position={[cx(m), MOD.top - 33, MOD.head + 0.4]} renderOrder={3}>
				<T.PlaneGeometry args={[24, 22]} />
				<T.MeshBasicMaterial map={haze} color={m.light} transparent opacity={0.42} blending={AdditiveBlending} depthWrite={false} toneMapped={false} />
			</T.Mesh>
		{/if}
		<Print w={MOD.w - 1} h={COVER_PRINT.h} position={[cx(m), COVER_PRINT.top - COVER_PRINT.h / 2, MOD.field + 0.1]} items={coverInk(m)} res={14} />

		{#each { length: m.channels } as _, ch (ch)}
			{@const tag = tagOf(m, ch)}
			{@const y = rowY(m, ch)}
			<!-- a discrete channel's lamp is its number: lit, the number is a white glow.
			     The analog covers are blank; their rows only answer the pointer. -->
			{#if m.kind !== 'ai' && on(tag)}
				<Led position={[left(m) + NUM_X, y, MOD.field + 0.3]} size={[m.channels > 12 ? 3.2 : 3.6, Math.min(2.6, pitch(m) * 0.72)]} on color="#ffffff" spread={3.2} />
			{/if}
			{#if hovered === tag}
				<T.Mesh position={[cx(m), y, MOD.field + 0.2]}>
					<T.PlaneGeometry args={[MOD.w - 2, Math.max(pitch(m), 3)]} />
					<T.MeshBasicMaterial color="#ffffff" transparent opacity={0.16} depthWrite={false} toneMapped={false} />
				</T.Mesh>
			{/if}
			<T.Mesh position={[cx(m), y, MOD.head + 0.6]} onpointerenter={() => onhover?.(describe(m, ch))} onpointerleave={() => onhover?.(null)}>
				<T.PlaneGeometry args={[MOD.w, pitch(m)]} />
				<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
			</T.Mesh>
		{/each}

		{#if m.kind === 'ai'}
			<T.Mesh position={[cx(m), (MOD.top + CALLOUT.y - CALLOUT.h / 2) / 2, MOD.head - 4]}>
				<T.BoxGeometry args={[0.5, CALLOUT.y - CALLOUT.h / 2 - MOD.top, 0.5]} />
				<T.MeshBasicMaterial color="#d8d6d0" toneMapped={false} />
			</T.Mesh>
			<Print w={CALLOUT.w} h={CALLOUT.h} position={[calloutX(m), CALLOUT.y, MOD.head - 4]} items={calloutInk(m)} plate={{ fill: 'rgba(22,22,21,0.94)', stroke: '#55554f', radius: 2 }} res={14} />
		{/if}
	{/each}

	<!-- the bar beside the chassis: two pushbuttons, a knob and a connector on an
	     aluminium plate. Bound on the simulated build only (`write`), to the
	     provisional wiring in ./bar.ts; on the controller it stays as drawn. -->
	<T.Mesh position={[BAR.x, (BAR.y0 + BAR.y1) / 2, 6]}>
		<RoundedBoxGeometry args={[26, BAR.y1 - BAR.y0 - 8, 16]} radius={2} />
		<T.MeshStandardMaterial color="#8d9297" roughness={0.45} metalness={0.6} />
	</T.Mesh>
	<T.Mesh position={[BAR.x, (BAR.y0 + BAR.y1) / 2, 15.5]}>
		<RoundedBoxGeometry args={[32, BAR.y1 - BAR.y0, 3]} radius={1} />
		<T.MeshStandardMaterial color={ALU} roughness={0.35} metalness={0.75} />
	</T.Mesh>
	{#each BAR.parts.slice(0, 2) as y, i}
		{@const tag = BAR_WIRING.buttons[i].tag}
		{@const kind = BAR_WIRING.buttons[i].kind}
		<T.Mesh position={[BAR.x, y, 17.6]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[8, 8.6, 1.6, 28]} />
			<T.MeshStandardMaterial color="#c3c7cb" roughness={0.25} metalness={0.9} />
		</T.Mesh>
		<!-- the plunger: in while its input is on -->
		<T.Mesh position={[BAR.x, y, pressed(i) ? 17.2 : 18.4]} rotation={[Math.PI / 2, 0, 0]}>
			<T.CylinderGeometry args={[5, 5, 1.6, 28]} />
			<T.MeshStandardMaterial color={pressed(i) ? '#7d8287' : '#9ea3a8'} roughness={0.3} metalness={0.9} />
		</T.Mesh>
		{#if write}
			<!-- its LED ring, lit by the tag it is wired to (bar.ts) -->
			<T.Mesh position={[BAR.x, y, 18.5]}>
				<T.TorusGeometry args={[6.4, 0.7, 8, 32]} />
				<T.MeshBasicMaterial color={ringLit(i) ? BAR_WIRING.buttons[i].ring.color : '#3a3d40'} toneMapped={false} />
			</T.Mesh>
			<T.Mesh
				bind:ref={buttonHits[i]}
				position={[BAR.x, y, 26]}
				onpointerenter={() => {
					cursor('pointer');
					onhover?.({ tag, text: () => (on(tag) ? 'on' : 'off') + (kind === 'latching' ? ' · click to latch on or off' : ' · hold to press') });
				}}
				onpointerleave={() => {
					cursor('');
					onhover?.(null);
				}}
			>
				<T.PlaneGeometry args={[24, 24]} />
				<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
			</T.Mesh>
		{/if}
	{/each}
	<T.Mesh position={[BAR.x, BAR.parts[2], 23]} rotation={[Math.PI / 2, 0, 0]}>
		<T.CylinderGeometry args={[8, 8.4, 12, 28]} />
		<T.MeshStandardMaterial color="#b9bdc1" roughness={0.3} metalness={0.85} />
	</T.Mesh>
	<!-- its pointer line, clockwise from 12 o'clock by knobAt -->
	<T.Group position={[BAR.x, BAR.parts[2], 29.1]} rotation={[0, 0, -knobAt]}>
		<T.Mesh position={[0, 5.1, 0]}>
			<T.PlaneGeometry args={[0.9, 6]} />
			<T.MeshBasicMaterial color="#1a1b1c" toneMapped={false} />
		</T.Mesh>
	</T.Group>
	{#if write}
		<!-- the simulated build's grip on the knob -->
		<T.Mesh
			bind:ref={knobHit}
			position={[BAR.x, BAR.parts[2], 30]}
			onpointerenter={() => {
				cursor('grab');
				onhover?.({ tag: KNOB.tag, text: () => `${num(KNOB.tag)?.toFixed(1) ?? '—'} ${KNOB.unit} · drag around to turn` });
			}}
			onpointerleave={() => {
				cursor('');
				onhover?.(null);
			}}
		>
			<T.PlaneGeometry args={[26, 26]} />
			<T.MeshBasicMaterial transparent opacity={0} depthWrite={false} />
		</T.Mesh>
	{/if}
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
