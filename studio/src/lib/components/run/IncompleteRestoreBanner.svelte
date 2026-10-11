<script lang="ts">
	// The restore hold, where the environment is looked at: a restore that
	// stopped the environment and did not complete keeps it down and
	// refuses deploys, because its data may be partial. While the restore
	// still runs this only says so; once it failed or was cancelled it
	// names both ways forward: restore again (the backups page), or accept
	// the data as it is, which a maintainer confirms by typing the name.
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { resolve } from '$app/paths';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { relativeTime } from '$lib/format';
	import { withEnv } from '$lib/urls';
	import { modal } from '$lib/stores/modal.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import AcceptRestoreModal, {
		modalOptions as acceptModalOptions
	} from '$lib/components/run/AcceptRestoreModal.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import type { Environment, Project } from '$lib/types/project';
	import type { IncompleteRestore } from '$lib/types/status';

	let {
		restore,
		env,
		project
	}: {
		restore: IncompleteRestore;
		env: Environment;
		project: Project;
	} = $props();

	const running = $derived(restore.status === 'running');
	const outcome = $derived(restore.status === 'cancelled' ? 'was cancelled' : 'failed');
	const acceptTitle = $derived(
		roleAtLeast(env.access, 'maintain')
			? undefined
			: requiredTitle('maintain', 'environment', env.name)
	);

	function openRun() {
		if (!restore.run_id) return;
		sidepanel.open(RunDetailPanel, { runId: restore.run_id }, { label: 'Run details' });
	}

	function openAccept() {
		modal.open(AcceptRestoreModal, { env, restore }, acceptModalOptions);
	}
</script>

<div
	role={running ? 'status' : 'alert'}
	class="mb-6.5 flex flex-col gap-3 rounded-[11px] border px-4 py-3.5 sm:flex-row sm:items-start {running
		? 'border-status-warning/25 bg-status-warning/10'
		: 'border-status-danger/25 bg-status-danger/8'}"
>
	<TriangleAlert
		size={18}
		class="mt-0.5 flex-none {running ? 'text-status-warning' : 'text-status-danger'}"
	/>
	<div class="min-w-0 flex-1">
		<p class="text-text-primary text-md font-medium">
			{#if running}
				Restoring snapshot <span class="font-mono">{restore.snapshot_id}</span>
			{:else}
				A restore {outcome}; {env.name} stays down
			{/if}
		</p>
		<p class="text-text-secondary mt-1 text-md">
			{#if running}
				{env.name} is down while its data is replaced, and deploys wait until the restore completes.
			{:else}
				The restore of snapshot <span class="font-mono">{restore.snapshot_id}</span>
				{outcome}
				{relativeTime(restore.finished_at ?? restore.started_at)}{#if restore.error}: {restore.error}{/if}.
				{#if restore.components.length > 0}
					<span class="font-mono">{restore.components.join(', ')}</span> may hold partial data, so
				{:else}
					Its data may be partial, so
				{/if}
				deploys are refused until a restore completes or the data is accepted as it is.
			{/if}
		</p>
	</div>
	<div class="flex flex-none flex-wrap gap-2">
		{#if restore.run_id}
			<Button size="sm" variant="ghost" onclick={openRun}>View run</Button>
		{/if}
		{#if !running}
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
			<Button
				size="sm"
				variant="secondary"
				href={withEnv(
					resolve('/(app)/projects/[project]/backups', { project: project.name }),
					env.name
				)}
			>
				Restore again
			</Button>
			<Button
				size="sm"
				variant="danger"
				disabled={!!acceptTitle}
				title={acceptTitle}
				onclick={openAccept}
			>
				Accept current data
			</Button>
		{/if}
	</div>
</div>
