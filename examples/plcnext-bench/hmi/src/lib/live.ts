// The page's one long-lived connection, held only while the page is on
// screen. A browser allows six HTTP/1.1 connections to an origin across
// ALL its tabs, and an event stream never finishes: six open copies of a
// screen, visible or not, and a seventh tab to the same controller waits
// for ever with no error. So the stream closes when the tab is hidden or
// the page is left, and reopens (with a fresh full frame) when it shows.
export interface Stream {
	start(): void;
	stop(): void;
}

/** Run `stream` while the document is visible. Returns the teardown. */
export function whileVisible(stream: Stream): () => void {
	const sync = () => (document.visibilityState === 'visible' ? stream.start() : stream.stop());
	const leave = () => stream.stop();
	// pagehide: a reload, a navigation, or the page going into the back/forward
	// cache; pageshow: coming back out of it.
	document.addEventListener('visibilitychange', sync);
	window.addEventListener('pagehide', leave);
	window.addEventListener('pageshow', sync);
	sync();
	return () => {
		document.removeEventListener('visibilitychange', sync);
		window.removeEventListener('pagehide', leave);
		window.removeEventListener('pageshow', sync);
		stream.stop();
	};
}
