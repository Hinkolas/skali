<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { api, ApiError } from '$lib/api/client';
	import type { RunTree } from '$lib/types/runs';
	import { runUnsettled } from '$lib/types/runs';
	import { openStream } from '$lib/sse';
	import { formatDuration, formatDateTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import RunStepTree from './RunStepTree.svelte';

	// Lives in the shared side panel; re-opening with a different runId
	// updates props in place, so all state below is keyed on runId.
	let { runId, close }: { runId: string; close: () => void } = $props();

	let tree = $state<RunTree | null>(null);
	let failed = $state(false);

	$effect(() => {
		const id = runId;
		tree = null;
		failed = false;
		let closed = false;
		// REST seed first (the stream re-sends full documents afterwards).
		api
			.get<RunTree>(`/v1/runs/${id}`)
			.then((doc) => {
				if (!closed && id === runId && !tree) tree = doc;
			})
			.catch(() => {
				if (!closed && id === runId) failed = true;
			});
		const handle = openStream<RunTree>({
			path: `/v1/runs/${id}/stream`,
			events: 'run',
			onEvent: (doc) => {
				if (id === runId) tree = doc;
			}
		});
		return () => {
			closed = true;
			handle.close();
		};
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

<div class="border-border-subtle flex items-start gap-2.5 border-b px-4.5 py-4">
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
					· {formatDuration(tree.run.started_at, tree.run.finished_at)}
				{/if}
				{#if tree.run.bypass_protection}
					· <span class="text-status-warning">bypassed protection</span>
				{/if}
			</div>
		{/if}
	</div>
	<div class="ml-auto flex flex-none items-center gap-2">
		{#if tree && runUnsettled(tree.run.status)}
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

<div class="min-h-0 flex-1 overflow-y-auto px-2.5 py-2.5">
	{#if failed}
		<div class="text-text-muted px-2 py-6 text-center text-base">
			Could not load this run. It may have been pruned.
		</div>
	{:else if !tree}
		<div class="text-text-ghost px-2 py-6 text-center text-base">Loading…</div>
	{:else}
		<RunStepTree steps={tree.steps} />
	{/if}
</div>
