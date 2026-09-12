<script lang="ts">
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import X from '@lucide/svelte/icons/x';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import type { RunTree } from '$lib/types/runs';
	import { runUnsettled } from '$lib/types/runs';
	import { openStream } from '$lib/sse';
	import { formatDuration, formatDateTime } from '$lib/format';
	import { clock } from '$lib/stores/clock.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import RunStepTree from './RunStepTree.svelte';

	// Lives in the shared side panel; re-opening with a different runId
	// updates props in place, so all state below is keyed on runId.
	let { runId, close }: { runId: string; close: () => void } = $props();

	let tree = $state<RunTree | null>(null);
	let failed = $state(false);

	// The reconciler's epilogue (deleting a purged environment, refreshing
	// the status projection) runs right after it concludes the run; a short
	// grace keeps the page refresh from racing it.
	const SETTLE_REFRESH_DELAY_MS = 750;

	$effect(() => {
		const id = runId;
		tree = null;
		failed = false;
		let closed = false;
		let unsettledSeen = false;
		// Every document goes through here so the settle transition is seen
		// once, whichever source delivers it. Page data behind the panel
		// (environment lists, health, last deploy) is load-time only, so the
		// run reaching its end is the moment to refresh it.
		const apply = (doc: RunTree) => {
			tree = doc;
			if (runUnsettled(doc.run.status)) {
				unsettledSeen = true;
			} else if (unsettledSeen) {
				unsettledSeen = false;
				setTimeout(() => void invalidateAll(), SETTLE_REFRESH_DELAY_MS);
			}
		};
		// REST seed first (the stream re-sends full documents afterwards).
		api
			.get<RunTree>(`/v1/runs/${id}`)
			.then((doc) => {
				if (!closed && id === runId && !tree) apply(doc);
			})
			.catch(() => {
				// A stream document that already arrived outranks a failed seed.
				if (!closed && id === runId && !tree) failed = true;
			});
		const handle = openStream<RunTree>({
			path: `/v1/runs/${id}/stream`,
			events: 'run',
			onEvent: (doc) => {
				if (id === runId) apply(doc);
			}
		});
		return () => {
			closed = true;
			handle.close();
		};
	});

	const unsettled = $derived(tree != null && runUnsettled(tree.run.status));

	// Top-level steps that reached an end state, as the bar's fill. A run
	// the reconciler has not picked up yet has no steps at all; the bar then
	// carries only its sheen, which is exactly the "something is happening"
	// the empty panel needs.
	const progress = $derived.by(() => {
		if (!tree || tree.steps.length === 0) return 0;
		const settled = tree.steps.filter(
			(s) => s.status !== 'pending' && s.status !== 'waiting' && s.status !== 'running'
		).length;
		return (settled / tree.steps.length) * 100;
	});

	const statusText: Record<string, string> = {
		pending: 'text-text-muted',
		running: 'text-status-warning',
		succeeded: 'text-status-success',
		failed: 'text-status-danger',
		cancelled: 'text-text-muted'
	};

	function cancelRun() {
		if (!tree) return;
		const run = tree.run;
		dialog.confirm({
			title: `Cancel this ${run.kind}?`,
			description:
				'Before promotion the run stops cleanly; after promotion the target is reverted.',
			confirmLabel: 'Cancel run',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.post(`/v1/runs/${run.id}/cancel`);
					toast.success('Run cancelled');
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not cancel the run');
					throw err;
				}
			}
		});
	}
</script>

<div class="border-border-subtle border-b px-4.5 py-4">
	<div class="flex items-start gap-2.5">
		<div class="min-w-0">
			<div class="text-text-primary flex items-center gap-2 text-xl font-semibold">
				{tree?.run.kind ?? 'run'}
				{#if tree}
					<span class="text-md font-medium {statusText[tree.run.status]}">{tree.run.status}</span>
				{/if}
			</div>
			{#if tree}
				<div class="font-mono text-text-faint mt-1 truncate text-xs">
					{tree.run.actor} · {formatDateTime(tree.run.created_at)}
					{#if tree.run.started_at}
						· {formatDuration(tree.run.started_at, tree.run.finished_at, clock.now)}
					{/if}
					{#if tree.run.bypass_protection}
						· <span class="text-status-warning">bypassed protection</span>
					{/if}
				</div>
			{/if}
		</div>
		<div class="ml-auto flex flex-none items-center gap-2">
			{#if unsettled}
				<Button size="sm" variant="ghost" onclick={cancelRun}>Cancel</Button>
			{/if}
			<button
				type="button"
				onclick={close}
				class="text-text-faint hover:text-text-primary cursor-pointer rounded-md p-1 transition-colors"
				aria-label="Close run details"
			>
				<X class="size-3.5" />
			</button>
		</div>
	</div>
	{#if !failed && (!tree || unsettled)}
		<div class="mt-3.5" role="progressbar" aria-label="Run progress" aria-valuenow={progress}>
			<ProgressBar pct={progress} active class="bg-status-warning" />
		</div>
	{/if}
</div>

<div class="min-h-0 flex-1 overflow-y-auto px-2.5 py-2.5">
	{#if failed}
		<div class="text-text-muted px-2 py-6 text-center text-base">
			Could not load this run. It may have been pruned.
		</div>
	{:else if !tree}
		<div class="text-text-muted flex items-center gap-2.5 px-2 py-1.75 text-base">
			<LoaderCircle size={15} class="text-text-ghost flex-none animate-spin" />
			loading the run
		</div>
	{:else if tree.steps.length === 0}
		{#if unsettled}
			<div class="text-text-muted flex items-center gap-2.5 px-2 py-1.75 text-base">
				<LoaderCircle size={15} class="text-status-warning flex-none animate-spin" />
				waiting for the reconciler to pick this up
			</div>
		{:else}
			<div class="text-text-faint px-2 py-6 text-center text-base">no steps were recorded</div>
		{/if}
	{:else}
		<RunStepTree steps={tree.steps} />
	{/if}
</div>
