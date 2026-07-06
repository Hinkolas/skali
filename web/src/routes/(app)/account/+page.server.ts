import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { SessionInfo } from '$lib/types/auth';
import type { PageServerLoad } from './$types';

// Self-service security page: open to every role. Sessions are server-loaded
// per navigation; mutations happen client-side through the /api proxy and
// re-run this load via invalidateAll().
export const load: PageServerLoad = async ({ locals, fetch }) => {
	const res = await apiFetch(fetch, locals.token, '/v1/auth/sessions');
	if (!res.ok) {
		error(502, 'Could not load sessions');
	}
	const { sessions } = (await res.json()) as { sessions: SessionInfo[] };
	return { sessions };
};
