<script lang="ts">
	// A deployment run part way through its rollout, as the Studio's run panel
	// shows it. Step titles are the ones the controller reports.
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';

	type Step = {
		title: string;
		state: 'succeeded' | 'running' | 'pending';
		duration?: string;
		progress?: string;
		child?: boolean;
	};

	const steps: Step[] = [
		{ title: 'Validate project definition', state: 'succeeded', duration: '1s' },
		{ title: 'Prepare artifacts', state: 'succeeded', duration: '42s' },
		{ title: 'Create revision', state: 'succeeded', duration: '1s' },
		{ title: 'Roll out revision', state: 'running', duration: '24s', progress: '3/5' },
		{ title: 'Provision databases.main', state: 'succeeded', duration: '12s', child: true },
		{
			title: 'Issue TLS certificate for web / main',
			state: 'succeeded',
			duration: '8s',
			child: true
		},
		{ title: 'Roll out web', state: 'running', duration: '4s', child: true },
		{ title: 'Verify health', state: 'pending', child: true },
		{ title: 'Activate revision', state: 'pending', child: true }
	];
</script>

<div
	class="w-full overflow-hidden rounded-[15px] border border-border-raised bg-surface-card leading-normal text-text-primary"
>
	<div
		class="flex flex-col gap-1.5 border-b border-border-subtle px-[19.8px] pt-[17.6px] pb-[15px]"
	>
		<div class="flex items-baseline gap-2.5">
			<span class="text-[17px] font-semibold">deployment</span>
			<span class="text-[13px] font-medium text-status-warning">running</span>
		</div>
		<div class="truncate font-mono text-[11px] text-text-faint">
			3f9a1c2e · Nicholas Hinke <span class="text-text-ghost">(you)</span> · 1m 08s
		</div>
		<div class="mt-1 h-[4.4px] overflow-hidden rounded-full bg-white/6">
			<div class="h-full w-[68%] bg-status-warning"></div>
		</div>
	</div>
	<div class="flex flex-col px-3 pt-2 pb-3 text-[14px]">
		{#each steps as step (step.title)}
			<div
				class={[
					'flex items-center gap-[11px] rounded-[9px] py-[7.7px] pr-[8.8px]',
					step.child ? 'pl-[26.8px]' : 'pl-[8.8px]',
					step.state === 'running' && !step.child && 'bg-white/3'
				]}
			>
				{#if step.state === 'succeeded'}
					<CircleCheck size={15} class="shrink-0 text-status-success" />
				{:else if step.state === 'running'}
					<LoaderCircle size={15} class="shrink-0 text-status-warning" />
				{:else}
					<CircleDashed size={15} class="shrink-0 text-text-ghost" />
				{/if}
				<span
					class={[
						'grow truncate',
						step.state === 'running' && 'text-text-primary',
						step.state === 'succeeded' && 'text-text-secondary',
						step.state === 'pending' && 'text-text-faint'
					]}
				>
					{step.title}
				</span>
				{#if step.progress}
					<span class="font-mono text-[11px] text-text-faint">{step.progress}</span>
				{/if}
				{#if step.duration}
					<span class="font-mono text-[11px] text-text-ghost">{step.duration}</span>
				{/if}
			</div>
		{/each}
	</div>
</div>
