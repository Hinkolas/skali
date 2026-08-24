import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { NodeMetrics, NodesStorage } from '$lib/types/metrics';
import type { PageServerLoad } from './$types';

// Nodes are instance-admin territory: the API already answers 403 to members
// (the shell layout degrades to an empty list), and the page refuses too so
// the sidebar's hiding is not the only line.
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to see nodes');
	}
	// Current usage per node; the shortest window carries the freshest bucket.
	const [metricsRes, storageRes] = await Promise.all([
		apiFetch(fetch, locals.token, '/v1/nodes/metrics?window=1h'),
		apiFetch(fetch, locals.token, '/v1/nodes/storage')
	]);
	return {
		nodeMetrics: metricsRes.ok ? ((await metricsRes.json()) as NodeMetrics) : null,
		nodeStorage: storageRes.ok ? ((await storageRes.json()) as NodesStorage) : null
	};
};
