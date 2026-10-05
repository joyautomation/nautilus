// The measurement the R&D plan's Phase 1 exit is judged on: frame rate, and
// how long a tag change takes to reach a pixel. Sampled in the browser and
// shown by <PerfHud>; nothing here affects rendering.
//
// Two latencies, because one clock is honest and the other is not always:
//
//   rx→pixel    the frame's ARRIVAL in the browser to the animation frame
//               after the one that drew it. One clock, so it is right from
//               any device: this is the render cost, and the primary figure.
//   ctrl→pixel  the controller's frame timestamp to that same pixel. Adds
//               the network hop, but subtracts two clocks — meaningful on
//               the same machine or with NTP, and pure clock skew without
//               (a laptop 43 s off reads "43155 ms"). Reported only while
//               plausible; otherwise the offset itself is reported.
//
// A hidden or background tab throttles requestAnimationFrame to ~1 Hz or
// stops it, and Threlte does not mount canvas children until a frame is
// drawn, so a background tab's numbers are nonsense. A sample is dropped
// if the page was hidden at ANY point between the frame's arrival and its
// pixel — not only at either end: a frame that arrives just before the tab
// is switched away waits, paused, for the tab to come back, and would
// otherwise record the whole time away as latency. Measure in a visible window.

/** Above this, a ctrl→pixel sample is clock offset, not latency. */
export const PLAUSIBLE_E2E_MS = 5000;

export class PerfSampler {
	/** Animation frames per second, updated once a second. */
	fps = $state(0);
	/** rx→pixel samples, ms (the last `window`). */
	latency = $state<number[]>([]);
	/** ctrl→pixel samples, ms, plausible ones only. */
	e2e = $state<number[]>([]);
	/** The last controller-minus-browser clock offset seen, ms; 0 while plausible. */
	clockOffsetMs = $state(0);
	#window: number;
	#raf = 0;
	#running = false;
	/** performance.now() of the last visibility change (either way). */
	#lastVisibilityChange = -Infinity;
	#onVisibility = () => {
		this.#lastVisibilityChange = performance.now();
	};

	constructor(window = 200) {
		this.#window = window;
	}

	/** p95 of rx→pixel, ms; 0 before any sample. */
	get p95(): number {
		return p95(this.latency);
	}
	/** p95 of ctrl→pixel, ms; 0 before any plausible sample. */
	get p95e2e(): number {
		return p95(this.e2e);
	}
	get samples(): number {
		return this.latency.length;
	}

	/**
	 * Call with a frame's controller timestamp (ms since the epoch) as it
	 * arrives. Both samples are taken two animation frames later: the first
	 * is the one that reacts to the frame, the second proves it was
	 * presented, so each figure is an upper bound on change -> pixel.
	 */
	onFrame(ts: number) {
		if (typeof document === 'undefined' || document.hidden) return;
		const rx = performance.now();
		requestAnimationFrame(() =>
			requestAnimationFrame(() => {
				// Hidden now, or hidden (and back) since the frame arrived.
				if (document.hidden || this.#lastVisibilityChange >= rx) return;
				const keep = this.#window - 1;
				this.latency = [...this.latency.slice(-keep), performance.now() - rx];
				const e2e = Date.now() - ts;
				if (Math.abs(e2e) <= PLAUSIBLE_E2E_MS) {
					this.e2e = [...this.e2e.slice(-keep), e2e];
					this.clockOffsetMs = 0;
				} else {
					this.clockOffsetMs = e2e;
				}
			})
		);
	}

	start() {
		if (this.#running) return;
		this.#running = true;
		if (typeof document !== 'undefined') document.addEventListener('visibilitychange', this.#onVisibility);
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
		if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', this.#onVisibility);
		cancelAnimationFrame(this.#raf);
	}

	reset() {
		this.latency = [];
		this.e2e = [];
		this.clockOffsetMs = 0;
	}
}

function p95(samples: number[]): number {
	if (!samples.length) return 0;
	const s = [...samples].sort((a, b) => a - b);
	return s[Math.min(s.length - 1, Math.floor(s.length * 0.95))];
}
