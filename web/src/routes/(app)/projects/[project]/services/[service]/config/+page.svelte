<script lang="ts">
	import ApplicationConfig from '$lib/components/config/ApplicationConfig.svelte';
	import BucketConfig from '$lib/components/config/BucketConfig.svelte';
	import DatabaseConfig from '$lib/components/config/DatabaseConfig.svelte';
	import type { PageData } from './$types';

	// The compiled definition of one service, read-only: the manifest owns
	// it, so this page explains what was declared rather than editing it.
	let { data }: { data: PageData } = $props();

	const service = $derived(data.service);
</script>

<svelte:head>
	<title>Config · {service.name} — skali</title>
</svelte:head>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Configuration</h2>
	<div class="text-text-muted text-md">
		declared in skali.yaml · edit the manifest and deploy to change it
	</div>
</div>

{#if service.type === 'application'}
	<ApplicationConfig {service} />
{:else if service.type === 'database'}
	<DatabaseConfig {service} />
{:else}
	<BucketConfig {service} />
{/if}
