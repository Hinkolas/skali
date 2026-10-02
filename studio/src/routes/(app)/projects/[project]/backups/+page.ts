// The project's snapshots come from the backup target, so the list can be
// missing for a reason worth showing (no target, target down) rather than
// just empty; keep the refusal code for the page to word. The runs seed
// the live backup activity of the selected environment.

import { apiFetch } from '$lib/api/client';
import type { BackupSnapshot } from '$lib/types/backups';
import type { Run } from '$lib/types/runs';
import type { PageLoad } from './$types';

export interface SnapshotsRefusal {
	status: number;
	code: string;
	message: string;
}

export const load: PageLoad = async ({ parent, fetch }) => {
	const { project, env } = await parent();
	const [snapshotsRes, runsRes] = await Promise.all([
		apiFetch(fetch, `/v1/projects/${project.id}/backups`),
		env && env.access !== 'none'
			? apiFetch(fetch, `/v1/environments/${env.id}/runs`)
			: Promise.resolve(null)
	]);

	let snapshots: BackupSnapshot[] | null = null;
	let refusal: SnapshotsRefusal | null = null;
	if (snapshotsRes.ok) {
		snapshots = ((await snapshotsRes.json()) as { snapshots: BackupSnapshot[] }).snapshots;
	} else {
		const body = (await snapshotsRes.json().catch(() => null)) as {
			error?: { code?: string; message?: string };
		} | null;
		refusal = {
			status: snapshotsRes.status,
			code: body?.error?.code ?? 'internal',
			message: body?.error?.message ?? snapshotsRes.statusText
		};
	}

	return {
		snapshots,
		refusal,
		runs: runsRes && runsRes.ok ? ((await runsRes.json()) as { runs: Run[] }).runs : null
	};
};
