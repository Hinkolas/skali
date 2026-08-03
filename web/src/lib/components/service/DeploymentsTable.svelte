<script lang="ts">
	import type { Deployment } from '$lib/mock/types';
	import Table from '$lib/components/ui/Table.svelte';

	let { deployments }: { deployments: Deployment[] } = $props();

	const grid = 'grid-cols-[0.8fr_2.4fr_1.2fr_1fr_1fr]';

	const statusClass: Record<Deployment['status'], { dot: string; text: string }> = {
		live: { dot: 'bg-status-success', text: 'text-status-success' },
		superseded: { dot: 'bg-text-ghost', text: 'text-text-muted' },
		failed: { dot: 'bg-status-warning', text: 'text-status-warning' }
	};
</script>

<Table columns={['Build', 'Source', 'Trigger', 'Duration', 'Status']} {grid}>
	{#each deployments as deployment (deployment.build)}
		<div
			class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {grid}"
		>
			<div class="font-mono text-text-primary text-md">{deployment.build}</div>
			<div class="text-text-secondary truncate pr-4 text-base">
				{deployment.message}
				<span class="font-mono text-text-faint text-xs">{deployment.sha}</span>
			</div>
			<div class="text-text-muted text-md">{deployment.trigger}</div>
			<div class="font-mono text-text-muted text-sm">{deployment.duration}</div>
			<div class="flex items-center gap-1.5 text-md {statusClass[deployment.status].text}">
				<span class="size-[8px] rounded-full {statusClass[deployment.status].dot}"></span>
				{deployment.status_label}
			</div>
		</div>
	{/each}
</Table>
