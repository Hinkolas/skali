import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { UpdateStatus } from '$lib/types/updates';
import type { PageLoad } from './$types';

// Admin only, like every system page. The status document is the whole
// page; the client keeps it fresh by polling while an update runs.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage updates');
	}
	const res = await apiFetch(fetch, '/v1/system/updates');
	if (!res.ok) error(502, 'Could not load update status');
	return { status: (await res.json()) as UpdateStatus };
};
