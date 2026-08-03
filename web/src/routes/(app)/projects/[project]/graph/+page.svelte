<script lang="ts">
	import Workflow from '@lucide/svelte/icons/workflow';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import GraphNode from '$lib/components/graph/GraphNode.svelte';
	import NodeDrawer from '$lib/components/graph/NodeDrawer.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const title = $derived(data.project.display_name || data.project.name);

	// Selection is page-local; no store needed.
	let selected = $state<string | null>(null);
	const selectedNode = $derived(data.graph?.nodes.find((n) => n.slug === selected) ?? null);
</script>

<svelte:head>
	<title>Service graph · {title} — skali</title>
</svelte:head>

<PageHeader title="Service graph">
	{#snippet subtitle()}
		How traffic and connections flow through {title} · click a node for details
	{/snippet}
	{#snippet actions()}
		<span title="Deploys run from the CLI for now: skali deploy">
			<Button variant="primary" disabled>Deploy</Button>
		</span>
	{/snippet}
</PageHeader>

{#if data.graph}
	<div
		class="bg-surface-canvas border-border-default relative mb-6 h-[704px] overflow-auto rounded-2xl border"
	>
		<div
			class="relative h-full"
			style:min-width="{data.graph.width}px"
			style:min-height="{data.graph.height}px"
			style:background-image="radial-gradient(rgb(255 255 255 / 0.05) 1px, transparent 1px)"
			style:background-size="24px 24px"
		>
			{#each data.graph.segments as segment, i (i)}
				<div
					class="border-accent/40 absolute border-dashed {segment.width
						? 'border-t-[1.5px]'
						: 'border-l-[1.5px]'}"
					style:left="{segment.left}px"
					style:top="{segment.top}px"
					style:width={segment.width ? `${segment.width}px` : undefined}
					style:height={segment.height ? `${segment.height}px` : undefined}
				></div>
			{/each}

			{#each data.graph.labels as label (label.text + label.left)}
				<div
					class="font-mono text-accent-light border-accent/30 absolute z-2 rounded-md border bg-[#16161d] px-1.5 py-px text-2xs"
					style:left="{label.left}px"
					style:top="{label.top}px"
				>
					{label.text}
				</div>
			{/each}

			{#each data.graph.nodes as node (node.slug)}
				<GraphNode
					{node}
					selected={selected === node.slug}
					onselect={(slug) => (selected = selected === slug ? null : slug)}
				/>
			{/each}

			{#if selectedNode}
				<NodeDrawer
					node={selectedNode}
					projectSlug={data.project.name}
					onclose={() => (selected = null)}
				/>
			{/if}
		</div>
	</div>
{:else}
	<EmptyState
		icon={Workflow}
		title="No service graph for {title} yet"
		description="the graph is only mocked for the storefront project"
	/>
{/if}
