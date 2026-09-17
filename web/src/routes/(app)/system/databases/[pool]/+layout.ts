import { error } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import { apiFetch } from '$lib/api/client';
import type { DatabasePoolDetailResponse } from '$lib/types/pools';
import type { LayoutLoad } from './$types';

// One pool's pages share the pool document: the header and tabs render
// from it and every tab reads it. invalidateAll() after a save re-runs
// this load, which is how the tuning form sees its own write land.
export const load: LayoutLoad = async ({ fetch, params, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage database pools');
	}
	const res = await apiFetch(fetch, `/v1/system/database-pools/${encodeURIComponent(params.pool)}`);
	if (res.status === 404) {
		error(404, 'Database pool not found');
	}
	if (!res.ok) {
		error(res.status, 'Could not load the database pool');
	}
	const { pool } = (await res.json()) as DatabasePoolDetailResponse;
	return {
		pool,
		crumbs: [
			{ label: 'System', href: resolve('/(app)/system') },
			{ label: 'Databases', href: resolve('/(app)/system/databases') },
			{ label: pool.name }
		]
	};
};
