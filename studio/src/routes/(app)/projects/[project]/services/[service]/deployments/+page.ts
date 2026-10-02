import { apiFetch } from '$lib/api/client';
import type { Run } from '$lib/types/runs';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
	const { env } = await parent();
	if (!env) return { runs: null };
	const res = await apiFetch(fetch, `/v1/environments/${env.id}/runs`);
	return { runs: res.ok ? ((await res.json()) as { runs: Run[] }).runs : null };
};
