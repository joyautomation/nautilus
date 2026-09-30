// The rack is the app: a device is looked at by pulling it out of the rack
// (/rack?focus=NODE1). This page was node1 on its own; its links still work.
import { redirect } from '@sveltejs/kit';

export const load = ({ url }) => {
	const q = new URLSearchParams(url.search);
	if (!q.has('focus')) q.set('focus', 'NODE1');
	redirect(307, `/rack?${q}`);
};
