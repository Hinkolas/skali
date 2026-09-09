import { apiFetch } from '$lib/api/client';
import type { ValueEntry } from '$lib/types/values';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
	const { env } = await parent();
	if (!env) return { values: [] };
	const res = await apiFetch(fetch, `/v1/environments/${env.id}/values`);
	return {
		values: res.ok ? ((await res.json()) as { values: ValueEntry[] }).values : []
	};
};
