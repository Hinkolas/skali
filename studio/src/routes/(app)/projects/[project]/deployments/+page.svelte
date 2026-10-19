<script lang="ts">
	import { untrack } from 'svelte';
	import { afterNavigate, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import Rocket from '@lucide/svelte/icons/rocket';
	import { relativeTime } from '$lib/format';
	import { readPref, writePref } from '$lib/prefs';
	import { envRuns } from '$lib/stores/envruns.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import {
		activeRun,
		followRun,
		runKindLabel,
		runUnsettled,
		type FollowedRun,
		type RunKind
	} from '$lib/types/runs';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Segmented from '$lib/components/ui/Segmented.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const envId = $derived(data.env?.id ?? null);
	const envName = $derived(data.env?.name ?? null);
	const runs = $derived(envRuns.of(envId) ?? data.runs ?? []);
	const active = $derived(activeRun(runs));

	// Rollouts are the runs that change what the environment runs. The rest
	// of the journal (backups, restores, rotations, drift repairs) is one
	// switch away.
	const ROLLOUT_KINDS: RunKind[] = ['deployment', 'rollback', 'restart', 'teardown', 'purge'];
	const FILTERS = ['rollouts', 'all'] as const;
	type Filter = (typeof FILTERS)[number];
	const FILTER_PREF = 'skali.deployments.filter';
	let filter = $state<Filter>(readPref(FILTER_PREF, FILTERS, 'rollouts'));
	const rollouts = $derived(runs.filter((r) => ROLLOUT_KINDS.includes(r.kind)));
	function setFilter(id: string) {
		filter = id as Filter;
		writePref(FILTER_PREF, filter);
	}

	const lastDeploy = $derived(
		runs.find((r) => r.kind === 'deployment' && r.status === 'succeeded') ?? null
	);

	// The card on top follows the run in flight. Once that run ends it
	// stays, showing the outcome the reader was waiting for, until another
	// run starts, the reader dismisses it, or the environment changes.
	let followed = $state<FollowedRun | null>(null);
	$effect(() => {
		const id = envId;
		const runId = active?.id ?? null;
		untrack(() => {
			const next = followRun(followed, id, runId);
			if (next !== followed) followed = next;
		});
	});
	const followedId = $derived(followed?.envId === envId ? followed.runId : null);
	const followedRun = $derived(runs.find((r) => r.id === followedId) ?? null);
	const followedSettled = $derived(followedRun !== null && !runUnsettled(followedRun.status));

	// ?run= names the run in the side panel, so every run has a link. Each
	// navigation that lands here sets the panel from the URL: a ?run= opens
	// its run, and its absence (a switched environment, the sidebar link)
	// closes a run panel. This waits for the landing because the shell
	// closes the previous page's panel then, and the router has to be
	// running before the URL can be rewritten. Between navigations the URL
	// follows the panel, whoever opened it. Rewrites are shallow, so no load
	// runs again, and page.url (also what Back restores) keeps the navigated
	// URL: the location is the one to read.
	let here = $state<string | null>(null);
	afterNavigate(() => {
		const url = new URL(window.location.href);
		here = url.pathname;
		const runId = url.searchParams.get('run');
		const shown = shownRunId();
		if (runId && shown !== runId) {
			sidepanel.open(RunDetailPanel, { runId }, { label: 'Run details' });
		} else if (!runId && shown) {
			sidepanel.close();
		}
	});

	function shownRunId(): string | null {
		const current = sidepanel.current;
		return current?.component === RunDetailPanel ? (current.props.runId as string) : null;
	}

	$effect(() => {
		const path = here;
		const runId = shownRunId();
		if (!path) return;
		untrack(() => {
			const url = new URL(window.location.href);
			if (url.pathname !== path || url.searchParams.get('run') === runId) return;
			if (runId) url.searchParams.set('run', runId);
			else url.searchParams.delete('run');
			// eslint-disable-next-line svelte/no-navigation-without-resolve -- same page, only ?run= rewritten
			replaceState(url, page.state);
		});
	});

	// The environment can also change without a navigation: a refresh that
	// resolves the default anew, say once a production environment exists.
	// A run of the previous environment does not stay open beside the next
	// one's list. Effects settle before afterNavigate runs, so a ?run= that
	// came with the change is opened again right after.
	let panelEnv: string | null = null;
	$effect(() => {
		const id = envId;
		untrack(() => {
			if (panelEnv !== null && panelEnv !== id && shownRunId()) sidepanel.close();
			panelEnv = id;
		});
	});
</script>

<svelte:head>
	<title>Deployments · {data.project.display_name || data.project.name} — skali</title>
</svelte:head>

<PageHeader title="Deployments">
	{#snippet subtitle()}
		{#if !envName}
			no environments yet
		{:else}
			<span class="font-mono">{envName}</span>
			·
			{#if active}
				<span class="text-status-warning">{runKindLabel(active.kind)} in progress</span>
			{:else if lastDeploy}
				last deployed {relativeTime(lastDeploy.finished_at ?? lastDeploy.created_at)}
			{:else}
				not deployed yet
			{/if}
		{/if}
	{/snippet}
</PageHeader>

{#if !data.env}
	<EmptyState
		icon={Rocket}
		title="No environments yet"
		description="Run `skali deploy` to create the first environment."
	/>
{:else}
	{#if followedId}
		<Card class="mb-6 flex max-h-[60vh] flex-col overflow-hidden">
			<RunDetailPanel
				runId={followedId}
				close={followedSettled ? () => (followed = null) : undefined}
			/>
		</Card>
	{/if}

	<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
		<div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h2 class="text-text-primary text-xl font-semibold">History</h2>
			<div class="text-text-muted text-md">
				{filter === 'rollouts'
					? 'deploys, rollbacks, restarts, and stops'
					: 'every run, including backups, restores, and rotations'}
			</div>
		</div>
		<Segmented
			label="Runs shown"
			value={filter}
			onchange={setFilter}
			segments={[
				{ id: 'rollouts', label: 'Rollouts', count: rollouts.length },
				{ id: 'all', label: 'All runs', count: runs.length }
			]}
		/>
	</div>

	<div class="pb-6">
		<RunsSection
			envId={data.env.id}
			seed={data.runs}
			kinds={filter === 'rollouts' ? ROLLOUT_KINDS : undefined}
			emptyTitle={filter === 'rollouts' ? 'No deploys yet' : 'No runs yet'}
			emptyDescription={filter === 'rollouts'
				? `deploys, rollbacks, restarts, and stops of ${data.env.name} appear here`
				: `every run of ${data.env.name} appears here`}
		/>
	</div>
{/if}
