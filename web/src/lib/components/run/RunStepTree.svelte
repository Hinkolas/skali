<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import CircleMinus from '@lucide/svelte/icons/circle-minus';
	import CircleSlash from '@lucide/svelte/icons/circle-slash';
	import CircleX from '@lucide/svelte/icons/circle-x';
	import Clock from '@lucide/svelte/icons/clock';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import type { NavIcon } from '$lib/navigation';
	import type { Step, StepStatus } from '$lib/types/runs';
	import { formatDuration } from '$lib/format';
	import RunStepTree from './RunStepTree.svelte';
	import StepLogView from './StepLogView.svelte';

	let { steps, depth = 0 }: { steps: Step[]; depth?: number } = $props();

	// Expansion is per step id, local to this level of the tree.
	let expanded = $state<Record<string, boolean>>({});

	const stepMeta: Record<StepStatus, { icon: NavIcon; class: string; spin?: boolean }> = {
		pending: { icon: CircleDashed, class: 'text-text-ghost' },
		waiting: { icon: Clock, class: 'text-status-warning' },
		running: { icon: LoaderCircle, class: 'text-status-warning', spin: true },
		succeeded: { icon: CircleCheck, class: 'text-status-success' },
		failed: { icon: CircleX, class: 'text-status-danger' },
		skipped: { icon: CircleMinus, class: 'text-text-ghost' },
		cancelled: { icon: CircleSlash, class: 'text-text-muted' }
	};
</script>

<div class="flex flex-col">
	{#each steps as step (step.id)}
		{@const meta = stepMeta[step.status]}
		{@const Icon = meta.icon}
		{@const tls = step.key.startsWith('tls:')}
		{@const isExpanded =
			expanded[step.id] ?? (tls && (step.status === 'waiting' || step.status === 'failed'))}
		<div style:padding-left="{depth * 18}px">
			<button
				type="button"
				aria-expanded={isExpanded}
				onclick={() => (expanded[step.id] = !isExpanded)}
				class="flex w-full cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.75 text-left transition-colors hover:bg-white/3"
			>
				<Icon size={15} class="flex-none {meta.class} {meta.spin ? 'animate-spin' : ''}" />
				<span class="text-text-secondary min-w-0 text-base {tls ? 'break-words' : 'truncate'}"
					>{step.title}</span
				>
				{#if step.progress_total}
					<span class="font-mono text-text-faint flex-none text-xs">
						{step.progress_current ?? 0}/{step.progress_total}
					</span>
				{/if}
				<span class="font-mono text-text-ghost ml-auto flex-none text-xs">
					{step.started_at ? formatDuration(step.started_at, step.finished_at) : ''}
				</span>
			</button>
			{#if isExpanded}
				<div class="mb-1.5 ml-6.5">
					<StepLogView
						stepId={step.id}
						live={step.status === 'running' || step.status === 'waiting'}
						{tls}
					/>
				</div>
			{/if}
			{#if step.children?.length}
				<RunStepTree steps={step.children} depth={depth + 1} />
			{/if}
		</div>
	{/each}
</div>
