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
		<div style:padding-left="{depth * 18}px">
			<button
				type="button"
				onclick={() => (expanded[step.id] = !expanded[step.id])}
				class="flex w-full cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.75 text-left transition-colors hover:bg-white/3"
			>
				<Icon size={15} class="flex-none {meta.class} {meta.spin ? 'animate-spin' : ''}" />
				<span class="text-text-secondary min-w-0 truncate text-base">{step.title}</span>
				{#if step.progress_total}
					<span class="font-mono text-text-faint flex-none text-xs">
						{step.progress_current ?? 0}/{step.progress_total}
					</span>
				{/if}
				<span class="font-mono text-text-ghost ml-auto flex-none text-xs">
					{step.started_at ? formatDuration(step.started_at, step.finished_at) : ''}
				</span>
			</button>
			{#if expanded[step.id]}
				<div class="mb-1.5 ml-6.5">
					<StepLogView stepId={step.id} live={step.status === 'running'} />
				</div>
			{/if}
			{#if step.children?.length}
				<RunStepTree steps={step.children} depth={depth + 1} />
			{/if}
		</div>
	{/each}
</div>
