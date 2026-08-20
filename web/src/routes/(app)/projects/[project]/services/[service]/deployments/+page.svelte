<script lang="ts">
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>Deployments · {data.service.name} — skali</title>
</svelte:head>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Runs</h2>
	<div class="text-text-muted text-md">
		the environment's full journal: deploys, rollbacks, teardowns
	</div>
</div>

<div class="pb-6">
	<RunsSection envId={data.env?.id ?? null} seed={data.runs} />
</div>
