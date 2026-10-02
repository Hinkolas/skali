import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { AuthUser } from '$lib/types/auth';
import type { PageLoad } from './$types';

// First real-data page: the user list is loaded per navigation;
// mutations happen client-side through the API and re-run this load
// via invalidateAll().
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage users');
	}

	const res = await apiFetch(fetch, '/v1/users');
	if (!res.ok) {
		error(res.status === 403 ? 403 : 502, 'Could not load users');
	}
	const { users } = (await res.json()) as { users: AuthUser[] };
	return { users };
};
