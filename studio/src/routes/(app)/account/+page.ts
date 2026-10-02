import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { SessionInfo } from '$lib/types/auth';
import type { PageLoad } from './$types';

// Self-service security page: open to every role. Sessions are loaded
// per navigation; mutations happen client-side through the API and
// re-run this load via invalidateAll().
export const load: PageLoad = async ({ fetch }) => {
	const res = await apiFetch(fetch, '/v1/auth/sessions');
	if (!res.ok) {
		error(502, 'Could not load sessions');
	}
	const { sessions } = (await res.json()) as { sessions: SessionInfo[] };
	return { sessions };
};
