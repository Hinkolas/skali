<script lang="ts">
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>{data.title} — skali</title>
</svelte:head>

<PageHeader title={data.title}>
	{#snippet subtitle()}
		Coming soon
	{/snippet}
</PageHeader>

<EmptyState
	icon={data.icon}
	title="{data.title} isn't available yet"
	description="this page will arrive in a future version"
/>
