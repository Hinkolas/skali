<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { slowestBuildSteps, type BuildInsights } from '$lib/types/runs';
	import { formatCount, formatMillis } from '$lib/format';

	// A build step's insights: the report the client uploads once the build
	// ended. Build output never streams into the step's log, so a running
	// build has nothing to show yet.
	let { stepId, live = false }: { stepId: string; live?: boolean } = $props();

	let insights = $state<BuildInsights | null>(null);
	let missing = $state(false);
	let failed = $state(false);
	let reload = $state(0);

	$effect(() => {
		const id = stepId;
		void reload;
		insights = null;
		missing = false;
		failed = false;
		if (live) return;
		let closed = false;
		void api
			.get<BuildInsights>(`/v1/steps/${id}/build-insights`)
			.then((report) => {
				if (!closed) insights = report;
			})
			.catch((error) => {
				if (closed) return;
				if (error instanceof ApiError && error.status === 404) missing = true;
				else failed = true;
			});
		return () => {
			closed = true;
		};
	});

	const cached = $derived(insights?.steps.filter((step) => step.status === 'cached').length ?? 0);
	const duration = $derived(
		insights
			? formatMillis(
					Math.round(
						new Date(insights.finished_at).getTime() - new Date(insights.started_at).getTime()
					)
				)
			: ''
	);
	const slowest = $derived(insights ? slowestBuildSteps(insights) : []);
</script>

{#if live}
	<div class="text-text-faint py-1 text-md">The build's insights arrive once it ends.</div>
{:else if failed}
	<div class="text-status-warning py-2 text-base">
		Could not load the build's insights. <button
			type="button"
			class="cursor-pointer underline"
			onclick={() => reload++}>Retry</button
		>
	</div>
{:else if missing}
	<div class="text-text-faint py-1 text-md">No insights available for this build.</div>
{:else if insights}
	<div class="flex flex-col gap-2 py-1">
		<div class="text-text-secondary flex flex-wrap gap-x-4 gap-y-1 text-sm">
			<span class={insights.outcome === 'succeeded' ? 'text-status-success' : 'text-status-danger'}
				>{insights.outcome}</span
			>
			<span>{duration}</span>
			<span>{insights.platform}</span>
			<span>{cached} of {insights.steps.length} steps cached</span>
		</div>
		{#if insights.error}
			<div class="font-mono text-status-danger text-sm break-words whitespace-pre-wrap">
				{insights.error}
			</div>
		{/if}
		{#if slowest.length > 0}
			<div class="flex flex-col">
				<div class="text-text-faint text-xs">Slowest steps</div>
				{#each slowest as step, index (index)}
					<div class="flex gap-2.5 py-0.5">
						<span class="font-mono text-text-faint w-14 flex-none text-right text-xs"
							>{step.seconds?.toFixed(1)}s</span
						>
						<span class="font-mono text-text-secondary min-w-0 truncate text-sm" title={step.name}
							>{step.name}</span
						>
					</div>
				{/each}
			</div>
		{/if}
		<details>
			<summary class="text-text-secondary cursor-pointer py-1 text-md">
				Build output ({formatCount(insights.log_lines)} lines{insights.log_truncated
					? ', start and end kept'
					: ''})
			</summary>
			<pre
				class="bg-surface-canvas border-border-subtle font-mono text-text-secondary max-h-96 overflow-auto rounded-[9px] border px-3 py-2 text-sm whitespace-pre-wrap [overflow-wrap:anywhere]">{insights.log}</pre>
		</details>
	</div>
{/if}
