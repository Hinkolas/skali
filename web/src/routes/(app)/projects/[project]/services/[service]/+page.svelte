<script lang="ts">
	import ApplicationOverview from '$lib/components/service/ApplicationOverview.svelte';
	import BucketOverview from '$lib/components/service/BucketOverview.svelte';
	import DatabaseOverview from '$lib/components/service/DatabaseOverview.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const service = $derived(data.service);
	const envId = $derived(data.env?.id ?? null);
</script>

<svelte:head>
	<title>{service.name} · {data.project.display_name || data.project.name} — skali</title>
</svelte:head>

{#if service.type === 'application'}
	<ApplicationOverview
		{service}
		services={data.services}
		{envId}
		runs={data.runs}
		storage={data.storage}
		temporaryStorage={data.temporaryStorage}
	/>
{:else if service.type === 'database'}
	<DatabaseOverview
		{service}
		services={data.services}
		connection={data.connection}
		{envId}
		storage={data.storage}
	/>
{:else}
	<BucketOverview
		{service}
		services={data.services}
		connection={data.bucketConnection}
		{envId}
		storage={data.storage}
	/>
{/if}
