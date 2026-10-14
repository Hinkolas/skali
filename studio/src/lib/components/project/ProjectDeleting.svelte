<script lang="ts">
	// Shown in place of every page of a project being deleted: each
	// environment is purging and the project goes with the last of them.
	// The project stream refreshes the list as environments disappear and
	// leaves for the project list once the project itself is gone.
	import { invalidateAll } from '$app/navigation';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { api, ApiError } from '$lib/api/client';
	import { roleAtLeast } from '$lib/access';
	import { runUnsettled, type RunsList } from '$lib/types/runs';
	import type { Environment, Project } from '$lib/types/project';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	let { project, environments }: { project: Project; environments: Environment[] } = $props();

	const projectAdmin = $derived(roleAtLeast(project.access.role, 'admin'));
	// An environment not yet purging means an earlier request was cut short
	// (a deployment window opened in between, the environment was busy):
	// repeating the delete picks up where it stopped.
	const interrupted = $derived(
		environments.some((e) => e.access !== 'none' && e.state !== 'releasing')
	);

	let resuming = $state(false);
	async function resume() {
		resuming = true;
		try {
			await api.del(`/v1/projects/${project.id}`);
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not resume the delete');
		} finally {
			resuming = false;
		}
	}

	let opening = $state<string | null>(null);
	async function showProgress(environment: Environment) {
		opening = environment.id;
		try {
			const { runs } = await api.get<RunsList>(`/v1/environments/${environment.id}/runs`);
			const run = runs.find((r) => r.kind === 'purge' && runUnsettled(r.status));
			if (run) sidepanel.open(RunDetailPanel, { runId: run.id }, { label: 'Run details' });
			else toast.info(`${environment.name} is about to be gone`);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not load the progress');
		} finally {
			opening = null;
		}
	}
</script>

<svelte:head>
	<title>Deleting {project.display_name || project.name} — skali</title>
</svelte:head>

<PageHeader title="Deleting {project.display_name || project.name}">
	{#snippet subtitle()}
		every environment is being deleted with its data; the project goes with the last of them
	{/snippet}
	{#snippet actions()}
		{#if interrupted && projectAdmin}
			<Button variant="danger" busy={resuming} onclick={resume}>Resume delete</Button>
		{/if}
	{/snippet}
</PageHeader>

<Card class="px-5 py-2.5">
	{#each environments as environment (environment.id)}
		{@const purging = environment.state === 'releasing'}
		<div class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0">
			{#if purging}
				<LoaderCircle size={13} class="text-status-warning flex-none animate-spin" />
			{:else}
				<span class="bg-text-ghost size-[8px] flex-none rounded-full"></span>
			{/if}
			<span class="font-mono text-text-primary text-md">{environment.name}</span>
			{#if purging}
				<Pill text="deleting" tone="warning" />
			{:else if environment.access === 'none'}
				<Pill text="locked" />
			{:else}
				<span title="the delete did not reach this environment; resume it">
					<Pill text="not started" tone="warning" />
				</span>
			{/if}
			{#if purging && environment.access !== 'none'}
				<div class="ml-auto">
					<Button
						size="sm"
						variant="ghost"
						busy={opening === environment.id}
						onclick={() => showProgress(environment)}
					>
						Progress
					</Button>
				</div>
			{/if}
		</div>
	{:else}
		<div class="text-text-muted flex items-center gap-2.5 py-2 text-base">
			<LoaderCircle size={15} class="text-status-warning flex-none animate-spin" />
			finishing up
		</div>
	{/each}
</Card>
