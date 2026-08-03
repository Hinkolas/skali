import { apiFetch } from '$lib/server/api';
import type { Run } from '$lib/types/runs';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ parent, locals, fetch }) => {
	const { env } = await parent();
	if (!env) return { runs: null };
	const res = await apiFetch(fetch, locals.token, `/v1/environments/${env.id}/runs`);
	return { runs: res.ok ? ((await res.json()) as { runs: Run[] }).runs : null };
};
