// The rack is the app (/rack, iso from the front). A device is looked at
// by pulling it out of the rack: /rack?focus=NODE1.
import { redirect } from '@sveltejs/kit';

export const load = ({ url }) => {
	redirect(307, `/rack${url.search}`);
};
