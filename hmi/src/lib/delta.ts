// Delta-frame merging for the realtime client — the client half of the
// controller's `/api/stream?delta=1` protocol.
//
// It lives here, as a plain function over a plain state object rather than
// inside `RealtimeClient`, for one reason: this is the only code in the kit
// that can silently show an operator a value the plant does not have. A
// merge bug does not throw, does not blank a screen and does not break a
// binding — it leaves one number frozen at whatever it last was, indefinitely,
// on a page that otherwise looks alive. That deserves to be readable and
// testable on its own, without a browser, a runtime or an EventSource.
//
// # The protocol, in full
//
// The controller sends whole frames until a client asks for deltas. A delta
// client's frames carry two extra fields:
//
//   - `seq` — this client's frame counter, from 1, reset on every new
//     connection.
//   - `full` — this frame is a complete snapshot: REPLACE, do not merge.
//     True on the first frame, on each periodic resync (~30 s), and
//     whenever the controller's tag set changed, which is the one
//     difference a delta cannot express (there is no way to say "this tag
//     is gone").
//
// Everything else on the frame — scan diagnostics, driver status, alarm
// counts, `quality` — is sent whole every time and simply passes through.
//
// # Why a gap means reconnect, not "ask for a resync"
//
// The server builds each delta from the generation that client was last
// brought up to date at, and advances that generation only when a frame is
// actually enqueued. A frame the server drops (a client too slow to keep
// up) therefore costs LATENCY, never content: the next frame carries what
// the dropped one would have. So a skipped `seq` cannot mean "a frame went
// missing" — it can only mean the transport mangled the stream, and a
// transport that did that once is not one to negotiate a repair over. The
// client reconnects, which yields a fresh full frame by construction.

/** A tag map as it crosses the wire: names to already-decoded JSON values. */
export type TagMap = Record<string, unknown>;

/**
 * The accumulated state of one delta subscription. Create with
 * `emptyDelta()`, hand the same object to every `mergeDelta` call, and
 * throw it away whenever the connection is replaced — a new connection is a
 * new subscription and starts from a new full frame.
 */
export interface DeltaState {
	/** Every tag the client currently believes in; null before the first
	 * full frame. Mutated in place — see mergeDelta's copy rule. */
	tags: TagMap | null;
	/** The last `seq` accepted; 0 before the first frame. */
	seq: number;
}

/** What one frame did. */
export interface DeltaResult<T> {
	/**
	 * The frame to publish — always COMPLETE, so nothing downstream needs
	 * to know deltas are on — or null when the frame must not be published
	 * because a gap was detected and the caller should reconnect.
	 */
	frame: T | null;
	/** A gap in `seq`: tear the stream down and reopen it. */
	gap: boolean;
	/** This frame was a full snapshot (first frame, resync, or shape
	 * change) rather than a merge. Informational — useful in a log line
	 * when someone is trying to see the protocol work. */
	full: boolean;
}

/** A fresh, empty subscription state. */
export function emptyDelta(): DeltaState {
	return { tags: null, seq: 0 };
}

/**
 * Merge one incoming frame into `state` and return the frame to publish.
 *
 * Three cases, in order:
 *
 *  1. **No `seq` field** — this controller does not implement deltas (or
 *     the frame is not nautilus-shaped at all). Pass the frame through
 *     untouched and leave `state` alone. This is what makes asking for
 *     deltas safe against any controller: an older one ignores the query
 *     parameter, sends whole frames, and this returns them verbatim.
 *  2. **`full`** — replace the accumulated tags outright.
 *  3. **otherwise** — merge this frame's `tags` over what we hold. A tag
 *     absent from a delta is UNCHANGED, never deleted; deletions arrive as
 *     a `full` frame, because that is the only way the protocol can
 *     express one.
 *
 * The published frame gets a shallow COPY of the accumulated tags, never
 * the accumulator itself. That is not politeness: the caller assigns the
 * frame to Svelte `$state`, which proxies it and caches that proxy against
 * the source object's identity — republishing one mutated object would
 * hand back a proxy whose per-property signals were never invalidated, and
 * every bound value on the screen would freeze at whatever it read first.
 * A fresh object per frame is also exactly what the non-delta path produces
 * (each tick is a fresh JSON.parse), so both paths behave identically.
 */
export function mergeDelta<T>(state: DeltaState, frame: T): DeltaResult<T> {
	const raw = frame as unknown as { tags?: TagMap; seq?: number; full?: boolean };
	if (typeof raw.seq !== 'number') {
		return { frame, gap: false, full: true };
	}
	if (state.seq !== 0 && raw.seq !== state.seq + 1) {
		return { frame: null, gap: true, full: false };
	}
	state.seq = raw.seq;
	// A first frame is treated as full whatever it claims: there is nothing
	// to merge into, and trusting a stream that opened mid-flight would
	// leave the screen holding whichever handful of tags happened to move.
	const full = raw.full === true || state.tags === null;
	let tags: TagMap;
	if (full) {
		tags = { ...(raw.tags ?? {}) };
	} else {
		tags = state.tags as TagMap;
		if (raw.tags) for (const k in raw.tags) tags[k] = raw.tags[k];
	}
	state.tags = tags;
	return { frame: { ...raw, tags: { ...tags } } as T, gap: false, full };
}
