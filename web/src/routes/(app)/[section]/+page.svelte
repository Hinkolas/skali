<script lang="ts">
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import SearchButton from '$lib/components/shell/SearchButton.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>{data.title} — skali</title>
</svelte:head>

<PageHeader title={data.title}>
	{#snippet subtitle()}
		Not designed yet — placeholder page
	{/snippet}
	{#snippet actions()}
		<SearchButton />
	{/snippet}
</PageHeader>

<EmptyState
	icon={data.icon}
	title="{data.title} isn't part of the prototype yet"
	description="this page will land in a later design pass"
/>
