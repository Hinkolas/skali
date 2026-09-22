// The one way the console starts a manual backup, shared by the project
// Backups tab and the database service header: confirm, accept the 202,
// follow the run in the side panel. The server checks deploy on the
// environment, that something runs there, and that the active revision
// declares something to snapshot; the helpers only word the refusal the
// console can already see.

import { api, ApiError } from '$lib/api/client';
import { requiredTitle, roleAtLeast } from '$lib/access';
import { hasStatefulServices } from '$lib/models/service';
import { dialog } from '$lib/stores/dialog.svelte';
import { sidepanel } from '$lib/stores/sidepanel.svelte';
import { toast } from '$lib/stores/toast.svelte';
import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
import type { ProjectDefinition } from '$lib/types/definition';
import type { Environment } from '$lib/types/project';

/** Wording of the refusal when the project declares nothing stateful. */
export const NOTHING_TO_BACK_UP = 'nothing to back up: no database, bucket, or volume is declared';

/**
 * A schedule may be on while the project declares nothing stateful: the
 * server accepts it and skips each fire. Rather than refusing the switch
 * (a project gains a database on its next deploy and the schedule should
 * already be there), the console says the schedule is idle and why. Shared
 * by the settings modal, the Backups page and the settings rows.
 */
export const SCHEDULE_IDLE = 'nothing to back up yet: no database, bucket, or volume is declared';
export const SCHEDULE_IDLE_DETAIL =
	'The schedule stays in place and snapshots start once a deploy adds one.';

/**
 * Why "Back up now" is disabled, or undefined when it may run. The draft
 * definition stands in for the active revision, the same way the Backups
 * tab reads policies from it.
 */
export function backupRefusal(
	env: Environment | null | undefined,
	definition: ProjectDefinition | null | undefined
): string | undefined {
	if (!env) return 'no environment selected';
	if (!roleAtLeast(env.access, 'deploy')) return requiredTitle('deploy', 'environment', env.name);
	if (!hasStatefulServices(definition ?? null)) return NOTHING_TO_BACK_UP;
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
