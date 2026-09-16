// The one way the console starts a manual backup, shared by the project
// Backups tab and the database service header: confirm, accept the 202,
// follow the run in the side panel. The server checks deploy on the
// environment and that something runs there; the helpers only word the
// refusal the console can already see.

import { api, ApiError } from '$lib/api/client';
import { requiredTitle, roleAtLeast } from '$lib/access';
import { dialog } from '$lib/stores/dialog.svelte';
import { sidepanel } from '$lib/stores/sidepanel.svelte';
import { toast } from '$lib/stores/toast.svelte';
import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
import type { Environment } from '$lib/types/project';

/** Why "Back up now" is disabled, or undefined when it may run. */
export function backupRefusal(env: Environment | null | undefined): string | undefined {
	if (!env) return 'no environment selected';
	if (!roleAtLeast(env.access, 'deploy')) return requiredTitle('deploy', 'environment', env.name);
	return undefined;
}

/** Confirm and start a manual snapshot of the environment. */
export function confirmBackup(env: Environment): void {
	dialog.confirm({
		title: `Back up ${env.name} now?`,
		description:
			'Every database, bucket, and application volume is snapshotted to the backup target. ' +
			'Manual snapshots are kept until you delete them.',
		confirmLabel: 'Back up',
		onConfirm: async () => {
			try {
				const res = await api.post<{ run_id: string; backup_id: string }>(
					`/v1/environments/${env.id}/backups`
				);
				toast.success(`Backing up ${env.name}`);
				sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
			} catch (err) {
				toast.error(err instanceof ApiError ? err.message : 'Could not start the backup');
			}
		}
	});
}
