<script lang="ts">
	import type { Snippet } from 'svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import PoolTabs from '$lib/components/system/PoolTabs.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import { poolPhase, poolSubtitle } from '$lib/types/pools';
	import type { LayoutData } from './$types';

	// Shared header for every pool page: name, observed phase and the
	// facts line persist while the tabs below switch content.
	let { data, children }: { data: LayoutData; children: Snippet } = $props();

	const phase = $derived(poolPhase(data.pool));
</script>

<PageHeader title={data.pool.name} mono>
	{#snippet titleTrailing()}
		<Pill text={phase.text} tone={phase.tone} />
	{/snippet}
	{#snippet subtitle()}
		<span class="font-mono text-text-faint text-md">{poolSubtitle(data.pool)}</span>
	{/snippet}
</PageHeader>

<PoolTabs pool={data.pool.name} />

{@render children()}
