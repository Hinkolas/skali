import { apiFetch } from '$lib/api/client';
import type { PoolMetrics } from '$lib/types/pools';
import type { PageLoad } from './$types';

// The overview seeds the 24h window; the page refetches other windows
// and polls. Null when the daemon serves no metrics (API-only mode).
export const load: PageLoad = async ({ fetch, params }) => {
	const res = await apiFetch(
		fetch,
		`/v1/system/database-pools/${encodeURIComponent(params.pool)}/metrics?window=24h`
	);
	return { metrics: res.ok ? ((await res.json()) as PoolMetrics) : null };
};
