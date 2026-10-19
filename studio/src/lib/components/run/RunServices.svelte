<script lang="ts">
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { formatDuration } from '$lib/format';
	import type { ServiceProgress } from '$lib/models/runservices';
	import { SERVICE_KIND_META } from '$lib/service-types';
	import { clock } from '$lib/stores/clock.svelte';
	import RunStepTree from './RunStepTree.svelte';
	import { deferredTLS, STEP_META } from './stepmeta';

	// A run's progress one row per service, as the CLI lists a deploy: the
	// row's state, a dot per step of its work, and the step it is on. A row
	// opens to its steps and their logs; a failed one starts open. Columns
	// line up where there is room, and dots past half the row wrap; a
	// narrow container (the side panel) drops the step title instead.
	let { rows }: { rows: ServiceProgress[] } = $props();

	let expanded = $state<Record<string, boolean>>({});
</script>

<div class="@container flex flex-col">
	{#each rows as row (row.service ?? '')}
		{@const id = row.service ?? ''}
		{@const meta = STEP_META[row.status]}
		{@const Icon = meta.icon}
		{@const kind = row.kind ? SERVICE_KIND_META[row.kind] : null}
		{@const isExpanded = expanded[id] ?? row.status === 'failed'}
		<div class={row.service === null ? 'border-border-subtle mt-1 border-t pt-1' : ''}>
			<button
				type="button"
				aria-expanded={isExpanded}
				onclick={() => (expanded[id] = !isExpanded)}
				class="group flex w-full cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.75 text-left transition-colors hover:bg-white/3"
			>
				<Icon size={15} class="flex-none {meta.class} {meta.spin ? 'animate-spin' : ''}" />
				<span class="sr-only">{row.status}</span>
				<span class="flex w-24 min-w-0 flex-none items-center gap-1.5 @xl:w-36">
					{#if kind}
						<kind.icon size={13} class="flex-none {kind.text}" aria-label={kind.label} />
					{/if}
					<span
						class="truncate text-base {row.service === null
							? 'text-text-muted'
							: 'text-text-primary font-medium'}"
						title={row.service ?? 'environment-wide steps'}>{row.name}</span
					>
				</span>
				<span
					class="flex min-w-0 flex-1 flex-wrap items-center gap-1 @md:max-w-1/2 @md:min-w-24 @md:flex-initial"
					aria-hidden="true"
				>
					{#each row.steps as step (step.id)}
						<span
							title="{step.title} · {step.status}"
							class="size-1.75 rounded-full {deferredTLS(step)
								? 'bg-status-warning'
								: STEP_META[step.status].dot}"
						></span>
					{/each}
				</span>
				<span class="text-text-muted hidden min-w-0 flex-1 truncate text-sm @md:block"
					>{row.current.title}</span
				>
				<span class="font-mono text-text-ghost flex-none text-xs">
					{formatDuration(row.started_at, row.finished_at, clock.now)}
				</span>
				<ChevronRight
					size={13}
					class="text-text-ghost group-hover:text-text-faint flex-none transition-transform {isExpanded
						? 'rotate-90'
						: ''}"
				/>
			</button>
			{#if isExpanded}
				<div class="mb-1.5 ml-6.5">
					<RunStepTree steps={row.steps} />
				</div>
			{/if}
		</div>
	{/each}
</div>
