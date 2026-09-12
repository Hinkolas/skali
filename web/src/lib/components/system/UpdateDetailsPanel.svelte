<script lang="ts">
	import { tick } from 'svelte';
	import X from '@lucide/svelte/icons/x';
	import { relativeTime } from '$lib/format';
	import Pill from '$lib/components/ui/Pill.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import {
		OPERATION_PHASE_LABEL,
		type UpdateStatus,
		type UpdateStepPhase,
		updateSummary
	} from '$lib/types/updates';

	// Lives in the shared side panel. The page re-opens it with a fresh
	// status document on every poll, which updates props in place.
	let {
		status,
		focusNode,
		close
	}: {
		status: UpdateStatus;
		/** A node id to scroll to and focus once, e.g. the failed step. */
		focusNode?: string;
		close: () => void;
	} = $props();

	const summary = $derived(updateSummary(status));
	const operation = $derived(status.operation);
	const showSteps = $derived(operation != null && operation.phase !== 'complete');

	// Controllers first, then by name: the order an update walks them.
	const nodes = $derived(
		status.nodes.toSorted((a, b) =>
			a.role === b.role ? a.name.localeCompare(b.name) : a.role === 'server' ? -1 : 1
		)
	);

	const stepDot: Record<UpdateStepPhase, string> = {
		pending: 'bg-white/15',
		running: 'bg-status-warning animate-pulse',
		complete: 'bg-status-success',
		failed: 'bg-status-danger'
	};
	const stepLabel: Record<UpdateStepPhase, string> = {
		pending: 'waiting for its turn',
		running: 'upgrading now',
		complete: 'upgraded',
		failed: 'upgrade failed'
	};
	const nodePhaseTone: Record<string, 'success' | 'warning' | 'neutral'> = {
		active: 'success',
		failed: 'warning'
	};

	$effect(() => {
		const id = focusNode;
		if (!id) return;
		tick().then(() => {
			const el = document.getElementById(`update-node-${id}`);
			el?.scrollIntoView({ block: 'center' });
			el?.focus();
		});
	});
</script>

<div class="border-border-subtle flex items-start gap-2.5 border-b px-4.5 py-4">
	<div class="min-w-0">
		<div class="text-text-primary text-xl font-semibold">Update details</div>
		<div class="font-mono text-text-faint mt-1 truncate text-xs">
			{#if operation && showSteps}
				{OPERATION_PHASE_LABEL[operation.phase] ?? operation.phase}
				{#if operation.target_version}· to {operation.target_version}{/if}
			{:else}
				cluster release {summary.converged_version ?? 'not verified'}
			{/if}
		</div>
	</div>
	<button
		type="button"
		onclick={close}
		class="text-text-faint hover:text-text-primary ml-auto cursor-pointer rounded-md p-1 transition-colors"
		aria-label="Close update details"
	>
		<X class="size-3.5" />
	</button>
</div>

<div class="min-h-0 flex-1 overflow-y-auto px-4.5 py-4">
	{#if status.last_error || operation?.error}
		<div
			class="border-status-warning/25 bg-status-warning/10 mb-5 rounded-[11px] border px-3.5 py-3 text-sm"
		>
			{#if status.last_error}
				<p class="text-status-warning font-medium">Last check</p>
				<p class="text-text-secondary mt-1 break-words">{status.last_error}</p>
			{/if}
			{#if operation?.error}
				<p class="text-status-warning font-medium {status.last_error ? 'mt-3' : ''}">Update</p>
				<p class="text-text-secondary mt-1 break-words">{operation.error}</p>
			{/if}
		</div>
	{/if}

	<h3 class="text-text-ghost mb-1 text-xs font-semibold tracking-wider uppercase">Platform</h3>
	<KeyValueRow k="skalid and console" v={status.installed.version} labelWidth="w-36" />
	<KeyValueRow
		k="Cluster release"
		v={summary.converged_version ?? 'not verified'}
		labelWidth="w-36"
	/>
	<p class="text-text-muted mt-2.5 text-md">
		The platform runs inside Kubernetes. A host agent runs on every node; controllers also run a
		coordinator.
	</p>

	<h3 class="text-text-ghost mt-6 mb-1 text-xs font-semibold tracking-wider uppercase">
		Nodes · {nodes.length}
	</h3>
	{#each nodes as node (node.id)}
		{@const step = operation?.steps.find((step) => step.node_id === node.id)}
		<div
			id={`update-node-${node.id}`}
			tabindex="-1"
			class="border-border-subtle border-b py-3.5 last:border-0 focus-visible:outline-none"
		>
			<div class="flex flex-wrap items-center gap-2">
				<span class="font-mono text-text-primary text-md break-all">{node.name}</span>
				<Pill text={node.role === 'server' ? 'controller' : 'worker'} />
				<Pill text={node.phase} tone={nodePhaseTone[node.phase] ?? 'neutral'} />
			</div>
			<dl class="text-text-muted mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
				<dt>Host agent</dt>
				<dd class="font-mono text-text-secondary break-all">
					{node.agent_version ?? 'not reported'}
					<span class="text-text-faint">
						· {node.last_seen ? relativeTime(node.last_seen) : 'never reported'}</span
					>
				</dd>
				{#if node.role === 'server'}
					<dt>Coordinator</dt>
					<dd class="font-mono text-text-secondary break-all">
						{node.coordinator_version ?? 'not reported'}
						<span class="text-text-faint">
							· {node.coordinator_last_seen
								? relativeTime(node.coordinator_last_seen)
								: 'never reported'}</span
						>
					</dd>
				{/if}
				<dt>Kubernetes</dt>
				<dd class="font-mono text-text-secondary break-all">
					{node.k3s_version ?? 'not reported'}
				</dd>
			</dl>
			{#if step && showSteps}
				<p class="text-text-muted mt-2 flex items-center gap-2 text-md">
					<span class="size-2 flex-none rounded-full {stepDot[step.phase]}"></span>
					{stepLabel[step.phase]}
				</p>
				{#if step.error}
					<p class="text-status-warning mt-1 text-sm break-words">{step.error}</p>
				{/if}
			{/if}
		</div>
	{/each}
</div>
