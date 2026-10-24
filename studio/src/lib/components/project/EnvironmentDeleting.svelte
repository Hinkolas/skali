<script lang="ts">
	// Shown in place of an environment's pages while it is being deleted:
	// nothing inside is worth acting on anymore, so the page is the delete's
	// progress instead. The project stream refreshes the layout when the
	// environment is gone, which moves the Studio to another environment.
	import { resolve } from '$app/paths';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { api } from '$lib/api/client';
	import { runUnsettled, type RunsList } from '$lib/types/runs';
	import type { Environment, Project } from '$lib/types/project';
	import { withEnv } from '$lib/urls';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	let {
		environment,
		environments,
		project
	}: { environment: Environment; environments: Environment[]; project: Project } = $props();

	// The delete's run: the newest one in flight. It is missing for a moment
	// before the reconciler adopts the decision, and for good once the run
	// concluded (the environment row, and its runs with it, go right after).
	let runId = $state<string | null>(null);
	let looked = $state(false);
	// Keyed on the id: every page refresh hands in a new environment object,
	// and refetching would remount the progress each time.
	const environmentId = $derived(environment.id);
	$effect(() => {
		const id = environmentId;
		runId = null;
		looked = false;
		let stale = false;
		api
			.get<RunsList>(`/v1/environments/${id}/runs`)
			.then(({ runs }) => {
				if (stale) return;
				const run =
					runs.find((r) => r.kind === 'purge' && runUnsettled(r.status)) ??
					runs.find((r) => runUnsettled(r.status));
				runId = run?.id ?? null;
			})
			.catch(() => {})
			.finally(() => {
				if (!stale) looked = true;
			});
		return () => {
			stale = true;
		};
	});

	// Somewhere to go meanwhile: another environment that is still there.
	const elsewhere = $derived(
		environments.find(
			(e) => e.id !== environment.id && e.access !== 'none' && e.state !== 'releasing'
		) ?? null
	);
</script>

<svelte:head>
	<title>Deleting {environment.name} · {project.display_name || project.name} — Skali</title>
</svelte:head>

<PageHeader title="Deleting {environment.name}" mono>
	{#snippet subtitle()}
		{project.display_name || project.name} · the environment is being deleted with its volumes, databases,
		buckets, values, revisions, and history
	{/snippet}
	{#snippet actions()}
		<div class="flex gap-2">
			<Button
				variant="ghost"
				href={withEnv(
					resolve('/(app)/projects/[project]/settings', { project: project.name }),
					environment.name
				)}
			>
				Project settings
			</Button>
			{#if elsewhere}
				<Button
					href={withEnv(
						resolve('/(app)/projects/[project]', { project: project.name }),
						elsewhere.name
					)}
				>
					Go to {elsewhere.name}
				</Button>
			{/if}
		</div>
	{/snippet}
</PageHeader>

<Card class="flex max-h-[70vh] flex-col overflow-hidden">
	{#if runId}
		<RunDetailPanel {runId} />
	{:else}
		<div class="text-text-muted flex items-center gap-2.5 px-4.5 py-4 text-base">
			<LoaderCircle size={15} class="text-status-warning flex-none animate-spin" />
			{looked ? 'waiting for the reconciler to pick this up' : 'loading the progress'}
		</div>
	{/if}
</Card>
