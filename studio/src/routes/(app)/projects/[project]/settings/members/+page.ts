import { apiFetch } from '$lib/api/client';
import type { Member } from '$lib/types/project';
import type { PageLoad } from './$types';

// The members list is returned by the API: each member carries their
// effective role per environment the caller may read. Mutations go through
// the Go API and re-run this load via invalidateAll().
export const load: PageLoad = async ({ parent, fetch }) => {
	const { project } = await parent();
	const res = await apiFetch(fetch, `/v1/projects/${project.id}/members`);
	return {
		members: res.ok ? ((await res.json()) as { members: Member[] }).members : []
	};
};
