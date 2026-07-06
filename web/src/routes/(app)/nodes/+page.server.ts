import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { Node } from '$lib/types/nodes';
import type { PageServerLoad } from './$types';

// Node list is server-loaded per navigation; mutations happen client-side
// through the /api proxy and re-run this load via invalidateAll(). The page
// also re-runs it on an interval for live-ish status.
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to manage nodes');
	}

	const res = await apiFetch(fetch, locals.token, '/v1/nodes');
	if (!res.ok) {
		error(res.status === 403 ? 403 : 502, 'Could not load nodes');
	}
	const { nodes } = (await res.json()) as { nodes: Node[] };
	return { nodes };
};
