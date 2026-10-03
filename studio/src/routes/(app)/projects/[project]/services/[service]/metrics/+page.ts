import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { EnvironmentMetrics } from '$lib/types/metrics';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
	const { env, service } = await parent();
	// Only applications have a metrics page so far; the tab is hidden for
	// databases and buckets, and their URL 404s like any other missing page.
	if (service.type !== 'application') error(404, 'Not found');
	if (!env) return { metrics: null };
	const res = await apiFetch(fetch, `/v1/environments/${env.id}/metrics?window=24h`);
	return { metrics: res.ok ? ((await res.json()) as EnvironmentMetrics) : null };
};
