import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { NodeMetrics, NodesStorage } from '$lib/types/metrics';
import type { PageLoad } from './$types';

// Admin only, like every system page: the API already answers 403 to members
// (the shell layout degrades to an empty list), and the page refuses too so
// the sidebar's hiding is not the only line.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to see nodes');
	}
	// Current usage per node; the shortest window carries the freshest bucket.
	const [metricsRes, storageRes] = await Promise.all([
		apiFetch(fetch, '/v1/nodes/metrics?window=1h'),
		apiFetch(fetch, '/v1/nodes/storage')
	]);
	return {
		nodeMetrics: metricsRes.ok ? ((await metricsRes.json()) as NodeMetrics) : null,
		nodeStorage: storageRes.ok ? ((await storageRes.json()) as NodesStorage) : null
	};
};
