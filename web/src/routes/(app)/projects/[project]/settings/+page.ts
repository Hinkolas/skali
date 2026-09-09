import { apiFetch } from '$lib/api/client';
import type { RevisionSummary, Target } from '$lib/types/revisions';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, fetch }) => {
	const { env } = await parent();
	if (!env) return { revisions: [], target: null };
	const [revisionsRes, targetRes] = await Promise.all([
		apiFetch(fetch, `/v1/environments/${env.id}/revisions`),
		apiFetch(fetch, `/v1/environments/${env.id}/target`)
	]);
	return {
		revisions: revisionsRes.ok
			? ((await revisionsRes.json()) as { revisions: RevisionSummary[] }).revisions
			: [],
		target: targetRes.ok ? ((await targetRes.json()) as { target: Target }).target : null
	};
};
