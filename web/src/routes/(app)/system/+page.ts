import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { BackupTarget } from '$lib/types/backups';
import type { UpdateStatus } from '$lib/types/updates';
import type { PageLoad } from './$types';

// The System hub is instance-admin territory: the API refuses
// members too, and the page refuses so the sidebar's hiding is not the only
// line. The update status and the backup target feed the card summaries.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to see system settings');
	}
	const [res, targetRes] = await Promise.all([
		apiFetch(fetch, '/v1/system/updates'),
		apiFetch(fetch, '/v1/system/backup-target')
	]);
	return {
		updates: res.ok ? ((await res.json()) as UpdateStatus) : null,
		backupTarget: targetRes.ok
			? ((await targetRes.json()) as { target: BackupTarget }).target
			: null
	};
};
