import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { AuthUser } from '$lib/types/auth';
import type { PageServerLoad } from './$types';

// First real-data page: the user list is server-loaded per navigation;
// mutations happen client-side through the /_api proxy and re-run this load
// via invalidateAll().
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to manage users');
	}

	const res = await apiFetch(fetch, locals.token, '/v1/users');
	if (!res.ok) {
		error(res.status === 403 ? 403 : 502, 'Could not load users');
	}
	const { users } = (await res.json()) as { users: AuthUser[] };
	return { users };
};
