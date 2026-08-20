import { apiFetch } from '$lib/server/api';
import type { EnvironmentMetrics } from '$lib/types/metrics';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ parent, locals, fetch }) => {
	const { env } = await parent();
	if (!env) return { metrics: null };
	const res = await apiFetch(fetch, locals.token, `/v1/environments/${env.id}/metrics?window=24h`);
	return { metrics: res.ok ? ((await res.json()) as EnvironmentMetrics) : null };
};
