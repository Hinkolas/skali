import { apiFetch } from '$lib/server/api';
import type { ValueEntry } from '$lib/types/values';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ parent, locals, fetch }) => {
	const { env } = await parent();
	if (!env) return { values: [] };
	const res = await apiFetch(fetch, locals.token, `/v1/environments/${env.id}/values`);
	return {
		values: res.ok ? ((await res.json()) as { values: ValueEntry[] }).values : []
	};
};
