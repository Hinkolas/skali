import { apiFetch } from '$lib/server/api';
import type { RevisionSummary, Target } from '$lib/types/revisions';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ parent, locals, fetch }) => {
	const { env } = await parent();
	if (!env) return { revisions: [], target: null };
	const [revisionsRes, targetRes] = await Promise.all([
		apiFetch(fetch, locals.token, `/v1/environments/${env.id}/revisions`),
		apiFetch(fetch, locals.token, `/v1/environments/${env.id}/target`)
	]);
	return {
		revisions: revisionsRes.ok
			? ((await revisionsRes.json()) as { revisions: RevisionSummary[] }).revisions
			: [],
		target: targetRes.ok ? ((await targetRes.json()) as { target: Target }).target : null
	};
};
