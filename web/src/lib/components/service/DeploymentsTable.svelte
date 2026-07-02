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
			<div class="font-mono text-text-primary text-[12px]">{deployment.build}</div>
			<div class="text-text-secondary truncate pr-4 text-[12.5px]">
				{deployment.message}
				<span class="font-mono text-text-faint text-[10px]">{deployment.sha}</span>
			</div>
			<div class="text-text-muted text-[11.5px]">{deployment.trigger}</div>
			<div class="font-mono text-text-muted text-[11px]">{deployment.duration}</div>
			<div class="flex items-center gap-1.5 text-[11.5px] {statusClass[deployment.status].text}">
				<span class="size-[7px] rounded-full {statusClass[deployment.status].dot}"></span>
				{deployment.status_label}
			</div>
		</div>
	{/each}
</Table>
