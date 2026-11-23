// The playground's camera (#724): a test pattern the kit draws itself, with no
// server and no video. Plain TS over a minimal 2-D context so the pattern is
// tested without a DOM (tests/testpattern.test.ts); VirtualCamera.svelte gives it a
// real canvas.

export interface Ctx {
	fillStyle: string | unknown;
	font: string;
	textBaseline: string;
	fillRect(x: number, y: number, w: number, h: number): void;
	fillText(text: string, x: number, y: number): void;
}

/** The seven classic bars: white, yellow, cyan, green, magenta, red, blue. */
export const BARS = ['#e6e6e6', '#e6e600', '#00e6e6', '#00e600', '#e600e6', '#e60000', '#0000e6'] as const;

/** Where the sweeping line is (0 to 1 of the height) at `t` seconds: down in four seconds, then again. */
export function sweep(t: number): number {
	const p = (t % 4) / 4;
	return p < 0 ? p + 1 : p;
}

/** "12:04:09", for the clock drawn in the corner. */
export function clock(d: Date): string {
	const p = (n: number) => String(n).padStart(2, '0');
	return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/**
 * Draw one frame: the bars, a line sweeping down, the camera's name, "SIMULATED" and
 * a clock. It never asks for anything: a virtual camera in the playground is this.
 */
export function drawTestPattern(ctx: Ctx, w: number, h: number, t: number, label: string, now: Date = new Date()): void {
	const bw = w / BARS.length;
	BARS.forEach((c, i) => {
		ctx.fillStyle = c;
		ctx.fillRect(i * bw, 0, bw + 1, h * 0.75);
	});
	ctx.fillStyle = '#101418';
	ctx.fillRect(0, h * 0.75, w, h * 0.25);
	ctx.fillStyle = '#ffffff';
	ctx.fillRect(0, sweep(t) * h, w, Math.max(2, h / 90));
	ctx.font = `${Math.round(h / 12)}px sans-serif`;
	ctx.textBaseline = 'top';
	ctx.fillStyle = '#ffffff';
	ctx.fillText(label, h * 0.04, h * 0.78);
	ctx.fillText(`SIMULATED  ${clock(now)}`, h * 0.04, h * 0.78 + h / 10);
}
