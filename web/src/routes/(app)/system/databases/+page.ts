import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { DatabasePoolsResponse } from '$lib/types/pools';
import type { PageLoad } from './$types';

// Admin only, like every system page: the API already answers 403 to
// members, and the page refuses too so the sidebar's hiding is not the only
// line.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage database pools');
	}
	const res = await apiFetch(fetch, '/v1/system/database-pools');
	if (!res.ok) {
		error(res.status, 'Could not load the database pools');
	}
	return { pools: ((await res.json()) as DatabasePoolsResponse).pools };
};
