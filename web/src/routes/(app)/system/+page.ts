import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { UpdateStatus } from '$lib/types/updates';
import type { PageLoad } from './$types';

// The System hub is instance-admin territory: the API refuses
// members too, and the page refuses so the sidebar's hiding is not the only
// line. Today it lists one section; the update status feeds its summary.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to see system settings');
	}
	const res = await apiFetch(fetch, '/v1/system/updates');
	return { updates: res.ok ? ((await res.json()) as UpdateStatus) : null };
};
