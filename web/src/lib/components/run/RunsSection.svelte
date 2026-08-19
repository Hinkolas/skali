<script lang="ts">
	import Rocket from '@lucide/svelte/icons/rocket';
	import { api, ApiError } from '$lib/api/client';
	import type { Run } from '$lib/types/runs';
	import { runUnsettled, type RunsList } from '$lib/types/runs';
	import { openStream } from '$lib/sse';
	import { formatDuration, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import RunDetailPanel from './RunDetailPanel.svelte';

	// The environment's run feed: seeded by the caller's load, kept live by
	// the runs-list SSE stream while mounted.
	let { envId, seed }: { envId: string | null; seed: Run[] | null } = $props();

	let live = $state<Run[] | null>(null);
	const runs = $derived(live ?? seed ?? []);

	$effect(() => {
		if (!envId) return;
		const handle = openStream<RunsList>({
			path: `/v1/environments/${envId}/runs/stream`,
			events: 'runs',
			onEvent: (data) => {
				live = data.runs;
			}
		});
		return () => {
			handle.close();
			live = null;
		};
	});

	const grid = 'grid-cols-[1fr_1.4fr_1.2fr_1fr_1fr_0.6fr]';

	const statusClass: Record<Run['status'], { dot: string; text: string }> = {
		pending: { dot: 'bg-text-ghost', text: 'text-text-muted' },
		running: { dot: 'bg-status-warning', text: 'text-status-warning' },
		succeeded: { dot: 'bg-status-success', text: 'text-status-success' },
		failed: { dot: 'bg-status-danger', text: 'text-status-danger' },
		cancelled: { dot: 'bg-text-ghost', text: 'text-text-muted' }
	};

	function openRun(run: Run) {
		sidepanel.open(RunDetailPanel, { runId: run.id }, { label: 'Run details' });
	}

	function cancelRun(run: Run) {
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

{#if runs.length === 0}
	<EmptyState
		icon={Rocket}
		title="No runs yet"
		description="deploys, rollbacks, and teardowns appear here"
	/>
{:else}
	<Table columns={['Run', 'Actor', 'Started', 'Duration', 'Status', '']} {grid}>
		{#each runs as run (run.id)}
			{@const status = statusClass[run.status]}
			<div
				class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {grid}"
			>
				<button
					type="button"
					onclick={() => openRun(run)}
					class="font-mono text-text-primary flex cursor-pointer items-center gap-2 text-left text-md hover:underline"
				>
					{run.kind}
					{#if run.bypass_protection}
						<span title="deployed into a promote-only environment on an admin's explicit bypass">
							<Pill text="bypassed protection" tone="warning" />
						</span>
					{/if}
				</button>
				<div class="text-text-muted truncate pr-2 text-md">{run.actor}</div>
				<div class="font-mono text-text-muted text-sm">
					{relativeTime(run.started_at ?? run.created_at)}
				</div>
				<div class="font-mono text-text-muted text-sm">
					{run.started_at ? formatDuration(run.started_at, run.finished_at) : ''}
				</div>
				<div class="flex items-center gap-1.5 text-md {status.text}">
					<span class="size-[8px] rounded-full {status.dot}"></span>
					{run.status}
				</div>
				<div class="flex justify-end">
					{#if runUnsettled(run.status)}
						<Button size="sm" variant="ghost" onclick={() => cancelRun(run)}>Cancel</Button>
					{/if}
				</div>
			</div>
		{/each}
	</Table>
{/if}
