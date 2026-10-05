// On a phone the boxes over the 3D (the alarm key, the legend, the
// scenarios) fold to chips in a row above the replay clock; a tap opens one
// as a sheet, one at a time, so the scene and the toolbar stay usable.
export const PHONE = '(max-width: 600px)';

class Sheets {
	phone = $state(false);
	open = $state<string | null>(null);
	constructor() {
		if (typeof window === 'undefined') return;
		const mq = window.matchMedia(PHONE);
		this.phone = mq.matches;
		mq.addEventListener('change', (e) => {
			this.phone = e.matches;
			this.open = null;
		});
	}
	toggle(id: string) {
		this.open = this.open === id ? null : id;
	}
}
export const sheets = new Sheets();
