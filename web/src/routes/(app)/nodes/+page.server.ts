import { error } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';

// Nodes are instance-admin territory: the API already answers 403 to members
// (the shell layout degrades to an empty list), and the page refuses too so
// the sidebar's hiding is not the only line.
export const load: PageServerLoad = async ({ locals }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to see nodes');
	}
	return {};
};
