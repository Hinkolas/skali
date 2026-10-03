import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { BackupTarget } from '$lib/types/backups';
import type { PageLoad } from './$types';

// Admin only, like every system page: the API already answers 403 to
// members, and the page refuses too so the sidebar's hiding is not the only
// line. An unconfigured target is a 404 the page renders as the empty form;
// any other failure is an error, never "no target yet".
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage the backup target');
	}
	const res = await apiFetch(fetch, '/v1/system/backup-target');
	if (res.status === 404) {
		return { target: null };
	}
	if (!res.ok) {
		error(res.status, 'Could not load the backup target');
	}
	return { target: ((await res.json()) as { target: BackupTarget }).target };
};
