<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import { modal } from '$lib/stores/modal.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import SearchButton from '$lib/components/shell/SearchButton.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import ProjectCard from '$lib/components/project/ProjectCard.svelte';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const nodeGrid = 'grid-cols-[1.4fr_1.6fr_1fr_1fr_1fr_0.9fr]';
</script>

<svelte:head>
	<title>Projects — skali</title>
</svelte:head>

<PageHeader title="Projects">
	{#snippet subtitle()}
		{data.org.project_count} projects across {data.org.node_count} nodes
	{/snippet}
	{#snippet actions()}
		<SearchButton />
		<Button
			variant="primary"
			onclick={() => modal.open(NewProjectModal, {}, newProjectModalOptions)}
		>
			<Plus size={15} strokeWidth={2.5} />
			New project
		</Button>
	{/snippet}
</PageHeader>

<div class="mb-7 grid grid-cols-3 gap-3.5">
	{#each data.projects as project (project.slug)}
		<ProjectCard {project} />
	{/each}
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-[16px] font-semibold">Nodes</h2>
	<div class="text-text-ghost text-[12px]">all healthy · daemon {data.org.version}</div>
</div>

<div class="pb-6">
	<Table columns={['Node', 'Address', 'CPU', 'Memory', 'Services', 'State']} grid={nodeGrid}>
		{#each data.nodes as node (node.name)}
			<div
				class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {nodeGrid}"
			>
				<div class="font-mono text-text-primary text-[12px]">{node.name}</div>
				<div class="font-mono text-text-faint text-[11px]">{node.address} · {node.provider}</div>
				<div class="font-mono text-text-muted text-[11px]">{node.cpu_pct}</div>
				<div class="font-mono text-text-muted text-[11px]">{node.memory}</div>
				<div class="font-mono text-text-muted text-[11px]">{node.service_count}</div>
				<div
					class="flex items-center gap-1.5 text-[11.5px] {node.state === 'healthy'
						? 'text-status-success'
						: 'text-status-warning'}"
				>
					<span
						class="size-[7px] rounded-full {node.state === 'healthy'
							? 'bg-status-success'
							: 'bg-status-warning'}"
					></span>
					{node.state}
				</div>
			</div>
		{/each}
	</Table>
</div>
