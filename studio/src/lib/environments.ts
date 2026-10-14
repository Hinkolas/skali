// The two environment teardown decisions, shared by the project settings
// list and the topbar Actions menu. Stop takes the workloads down and keeps
// everything else; delete is the one-way purge. Both are persisted
// decisions the reconciler executes, so the Studio only confirms and
// starts them; the project stream refreshes every view as they progress.

import { invalidateAll } from '$app/navigation';
import { api, ApiError } from '$lib/api/client';
import { dialog } from '$lib/stores/dialog.svelte';
import { sidepanel } from '$lib/stores/sidepanel.svelte';
import { toast } from '$lib/stores/toast.svelte';
import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
import type { Environment } from '$lib/types/project';

export const STOP_ENVIRONMENT_TITLE =
	'Takes the running workloads down but keeps values, revisions, and volumes. The next deploy starts it again.';
export const STOPPED_ENVIRONMENT_TITLE = 'Already stopped; the next deploy starts it again.';
export const DELETE_ENVIRONMENT_TITLE =
	'Deletes the environment with its volumes, databases, buckets, values, revisions, and history. One-way.';

type Target = Pick<Environment, 'id' | 'name'>;

export function confirmStopEnvironment(env: Target) {
	dialog.confirm({
		title: `Stop ${env.name}?`,
		description:
			'Takes the running workloads down. Values, revisions, and volumes stay, and the next deploy starts the environment again.',
		confirmLabel: 'Stop environment',
		variant: 'danger',
		onConfirm: async () => {
			try {
				const result = await api.post<{ run_id: string }>(`/v1/environments/${env.id}/teardown`);
				toast.success(`Stopping ${env.name}`);
				sidepanel.open(RunDetailPanel, { runId: result.run_id }, { label: 'Run details' });
				await invalidateAll();
			} catch (err) {
				toast.error(err instanceof ApiError ? err.message : 'Could not stop the environment');
				throw err;
			}
		}
	});
}

// No side panel here: a deleting environment shows its progress in place,
// in the settings list and instead of the environment's pages.
export function confirmDeleteEnvironment(env: Target) {
	dialog.confirm({
		title: `Delete ${env.name}?`,
		description:
			'Deletes the namespace with its volumes, the databases and buckets with their data, and the environment with all values, revisions, and history. This is one-way.',
		confirmLabel: 'Delete environment',
		variant: 'danger',
		typeToConfirm: env.name,
		onConfirm: async () => {
			try {
				await api.post(`/v1/environments/${env.id}/teardown`, { purge: true });
				toast.success(`Deleting ${env.name}`);
				await invalidateAll();
			} catch (err) {
				toast.error(err instanceof ApiError ? err.message : 'Could not delete the environment');
				throw err;
			}
		}
	});
}
