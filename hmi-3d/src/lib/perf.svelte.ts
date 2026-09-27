// The measurement the R&D plan's Phase 1 exit is judged on: frame rate, and
// how long a tag change takes to reach a pixel. Both are sampled in the
// browser and shown by <PerfHud>; nothing here affects rendering.
//
// A hidden or background tab throttles requestAnimationFrame to ~1 Hz or
// stops it, and Threlte does not mount canvas children until a frame is
// drawn, so a background tab's numbers are nonsense. Frames that arrive
// while `document.hidden` are not sampled. Measure in a visible window.

export class PerfSampler {
	/** Animation frames per second, updated once a second. */
	fps = $state(0);
	/** The last `window` ts->pixel samples, ms. */
	latency = $state<number[]>([]);
	#window: number;
	#raf = 0;
	#running = false;

	constructor(window = 200) {
		this.#window = window;
	}

	/** p95 of the latency samples, ms; 0 before any sample. */
	get p95(): number {
		if (!this.latency.length) return 0;
		const s = [...this.latency].sort((a, b) => a - b);
		return s[Math.min(s.length - 1, Math.floor(s.length * 0.95))];
	}

	get samples(): number {
		return this.latency.length;
	}

	/**
	 * Call with a frame's controller timestamp (ms since the epoch) as it
	 * arrives. The sample is taken two animation frames later: the first is
	 * the one that reacts to the frame, the second proves it was presented,
	 * so the figure is an upper bound on tag-change -> pixel. It is only
	 * meaningful when the browser and the controller share a clock (the
	 * same machine, or both NTP-synced); from another device read it as
	 * "plus clock skew".
	 */
	onFrame(ts: number) {
		if (typeof document === 'undefined' || document.hidden) return;
		requestAnimationFrame(() =>
			requestAnimationFrame(() => {
				if (document.hidden) return;
				const ms = Date.now() - ts;
				this.latency = [...this.latency.slice(-(this.#window - 1)), ms];
			})
		);
	}

	start() {
		if (this.#running) return;
		this.#running = true;
		let frames = 0;
		let last = performance.now();
		const tick = (now: number) => {
			frames++;
			if (now - last >= 1000) {
				this.fps = Math.round((frames * 1000) / (now - last));
				frames = 0;
				last = now;
			}
			this.#raf = requestAnimationFrame(tick);
		};
		this.#raf = requestAnimationFrame(tick);
	}

	stop() {
		this.#running = false;
		cancelAnimationFrame(this.#raf);
	}

	reset() {
		this.latency = [];
	}
}
