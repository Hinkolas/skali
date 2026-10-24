import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { BackupTarget, BackupTargetSummary } from '$lib/types/backups';
import type { PageLoad } from './$types';

// Admin only, like every system page: the API already answers 403 to
// members, and the page refuses too so the sidebar's hiding is not the only
// line. An unconfigured target is a 404 the page renders as its empty
// state; any other failure is an error, never "no target yet".
//
// The summary is returned as a promise, not awaited (see deferred()): it
// asks the bucket, which can take a while or time out, and the page paints
// the stored target first. It resolves to null where the daemon offers no
// summary (API-only mode, an older release) or could not build one; the
// page then shows the target alone.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage backups');
	}
	const res = await apiFetch(fetch, '/v1/system/backup-target');
	if (res.status === 404) {
		return { target: null, summary: Promise.resolve(null) };
	}
	if (!res.ok) {
		error(res.status, 'Could not load the backup target');
	}
	const target = ((await res.json()) as { target: BackupTarget }).target;
	const summary = (async (): Promise<BackupTargetSummary | null> => {
		const summaryRes = await apiFetch(fetch, '/v1/system/backup-target/summary');
		return summaryRes.ok ? ((await summaryRes.json()) as BackupTargetSummary) : null;
	})().catch(() => null);
	return { target, summary };
};
