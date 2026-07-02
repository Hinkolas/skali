<script lang="ts">
	import ApplicationOverview from '$lib/components/service/ApplicationOverview.svelte';
	import DatabaseOverview from '$lib/components/service/DatabaseOverview.svelte';
	import GenericServiceOverview from '$lib/components/service/GenericServiceOverview.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const service = $derived(data.service);
</script>

<svelte:head>
	<title>{service.name} · {data.project.name} — skali</title>
</svelte:head>

{#if service.type === 'application'}
	<ApplicationOverview {service} services={data.services} />
{:else if service.type === 'database'}
	<DatabaseOverview {service} />
{:else}
	<GenericServiceOverview {service} />
{/if}
