<script lang="ts">
	import { formatDuration } from '$lib/format';
	import { clock } from '$lib/stores/clock.svelte';
	import RunStepTree from './RunStepTree.svelte';
	import StepLogView from './StepLogView.svelte';
	import BuildInsightsView from './BuildInsightsView.svelte';
	import { isBuildStep, type Step } from '$lib/types/runs';
	import { deferredTLS, STEP_META } from './stepmeta';

	let { steps, depth = 0 }: { steps: Step[]; depth?: number } = $props();

	// Expansion is per step id, local to this level of the tree.
	let expanded = $state<Record<string, boolean>>({});
</script>

<div class="flex flex-col">
	{#each steps as step (step.id)}
		{@const meta = STEP_META[step.status]}
		{@const Icon = meta.icon}
		{@const tls = step.key.startsWith('tls:')}
		<!-- A skipped TLS checkpoint is a deferred certificate: its snapshot is the only explanation, so it opens like a failure. -->
		{@const isExpanded =
			expanded[step.id] ??
			(tls && (step.status === 'waiting' || step.status === 'failed' || step.status === 'skipped'))}
		<div style:padding-left="{depth * 18}px">
			<button
				type="button"
				aria-expanded={isExpanded}
				onclick={() => (expanded[step.id] = !isExpanded)}
				class="flex w-full cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.75 text-left transition-colors hover:bg-white/3"
			>
				<!-- A skipped TLS checkpoint is a deferral, not an omission: it reads as a warning. -->
				<Icon
					size={15}
					class="flex-none {deferredTLS(step) ? 'text-status-warning' : meta.class} {meta.spin
						? 'animate-spin'
						: ''}"
				/>
				<span class="text-text-secondary min-w-0 text-base {tls ? 'break-words' : 'truncate'}"
					>{step.title}</span
				>
				{#if step.progress_total}
					<span class="font-mono text-text-faint flex-none text-xs">
						{step.progress_current ?? 0}/{step.progress_total}
					</span>
				{/if}
				<span class="font-mono text-text-ghost ml-auto flex-none text-xs">
					{step.started_at ? formatDuration(step.started_at, step.finished_at, clock.now) : ''}
				</span>
			</button>
			{#if isExpanded}
				<div class="mb-1.5 ml-6.5">
					{#if isBuildStep(step.key)}
						<BuildInsightsView
							stepId={step.id}
							live={step.status === 'running' ||
								step.status === 'waiting' ||
								step.status === 'pending'}
						/>
					{:else}
						<StepLogView
							stepId={step.id}
							live={step.status === 'running' || step.status === 'waiting'}
							{tls}
						/>
					{/if}
				</div>
			{/if}
			{#if step.children?.length}
				<RunStepTree steps={step.children} depth={depth + 1} />
			{/if}
		</div>
	{/each}
</div>
