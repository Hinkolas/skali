// Sidebar/org data for the whole authenticated area. Universal load over the
// mock layer (swaps to real API calls later); spreads the server load's data
// (`user`) through.

import { getOrg, listNodes, listProjects } from '$lib/mock';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ data }) => {
	return {
		...data,
		org: await getOrg(),
		projects: await listProjects(),
		nodes: await listNodes()
	};
};
