// Lettering for the model: text drawn on a 2D canvas with the system font
// and used as a texture, so nothing is fetched at run time (the bench LAN
// has no route to a font CDN) and the print sits on the part it names.
export interface Ink {
	text: string;
	/** Millimetres from the face's top-left corner; y is the baseline's middle. */
	x: number;
	y: number;
	/** Cap height, mm. */
	size: number;
	color?: string;
	align?: 'left' | 'center' | 'right';
	weight?: number;
	italic?: boolean;
	mono?: boolean;
	/** Degrees, clockwise, about (x, y): -90 reads bottom to top. */
	rotate?: number;
	/** Extra space between letters, mm. */
	spacing?: number;
}

export interface Plate {
	fill: string;
	stroke?: string;
	/** Corner radius, mm. */
	radius?: number;
}

const SANS = 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif';
const MONO = 'ui-monospace, "SF Mono", "Cascadia Mono", Menlo, Consolas, monospace';

/** Anything that is not lettering (a terminal block, a socket), drawn in mm under the text. */
export type Paint = (ctx: CanvasRenderingContext2D) => void;

/** Draw `items` on `canvas`, sized w x h mm at `res` pixels per mm. */
export function drawPrint(canvas: HTMLCanvasElement, w: number, h: number, items: Ink[], res: number, plate?: Plate, paint?: Paint): void {
	const pw = Math.max(2, Math.round(w * res));
	const ph = Math.max(2, Math.round(h * res));
	if (canvas.width !== pw) canvas.width = pw;
	if (canvas.height !== ph) canvas.height = ph;
	const ctx = canvas.getContext('2d');
	if (!ctx) return;
	ctx.setTransform(1, 0, 0, 1, 0, 0);
	ctx.clearRect(0, 0, pw, ph);
	ctx.scale(res, res);
	if (plate) {
		const r = plate.radius ?? 0;
		const inset = plate.stroke ? 0.25 : 0;
		ctx.beginPath();
		ctx.roundRect(inset, inset, w - inset * 2, h - inset * 2, r);
		ctx.fillStyle = plate.fill;
		ctx.fill();
		if (plate.stroke) {
			ctx.lineWidth = 0.35;
			ctx.strokeStyle = plate.stroke;
			ctx.stroke();
		}
	}
	paint?.(ctx);
	for (const it of items) {
		ctx.save();
		ctx.translate(it.x, it.y);
		if (it.rotate) ctx.rotate((it.rotate * Math.PI) / 180);
		// A font's cap height is about 0.72 of its em.
		ctx.font = `${it.italic ? 'italic ' : ''}${it.weight ?? 500} ${it.size / 0.72}px ${it.mono ? MONO : SANS}`;
		if (it.spacing) ctx.letterSpacing = `${it.spacing}px`;
		ctx.textAlign = it.align ?? 'left';
		ctx.textBaseline = 'middle';
		ctx.fillStyle = it.color ?? '#ffffff';
		ctx.fillText(it.text, 0, 0);
		ctx.restore();
	}
}
