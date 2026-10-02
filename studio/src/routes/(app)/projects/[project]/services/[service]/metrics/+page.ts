import { apiFetch } from '$lib/api/client';
import type { EnvironmentMetrics } from '$lib/types/metrics';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
	const { env } = await parent();
	if (!env) return { metrics: null };
	const res = await apiFetch(fetch, `/v1/environments/${env.id}/metrics?window=24h`);
	return { metrics: res.ok ? ((await res.json()) as EnvironmentMetrics) : null };
};
