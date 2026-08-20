import { apiFetch } from '$lib/server/api';
import type { Member } from '$lib/types/project';
import type { PageServerLoad } from './$types';

// The members list is server-rendered by the API: each member carries their
// effective role per environment the caller may read. Mutations go through
// the /_api proxy and re-run this load via invalidateAll().
export const load: PageServerLoad = async ({ parent, locals, fetch }) => {
	const { project } = await parent();
	const res = await apiFetch(fetch, locals.token, `/v1/projects/${project.id}/members`);
	return {
		members: res.ok ? ((await res.json()) as { members: Member[] }).members : []
	};
};
