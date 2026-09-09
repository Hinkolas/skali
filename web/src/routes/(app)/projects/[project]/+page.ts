import { apiFetch } from '$lib/api/client';
import type { EnvironmentMetrics, ProjectStorage } from '$lib/types/metrics';
import type { PageLoad } from './$types';

// Usage tiles for the selected environment plus the project's current
// storage footprints; null degrades to placeholders.
export const load: PageLoad = async ({ parent, fetch }) => {
	const { env, project } = await parent();
	if (!env) return { metrics: null, storage: null };
	const [metricsRes, storageRes] = await Promise.all([
		apiFetch(fetch, `/v1/environments/${env.id}/metrics?window=24h`),
		apiFetch(fetch, `/v1/projects/${project.id}/storage`)
	]);
	return {
		metrics: metricsRes.ok ? ((await metricsRes.json()) as EnvironmentMetrics) : null,
		storage: storageRes.ok ? ((await storageRes.json()) as ProjectStorage) : null
	};
};
