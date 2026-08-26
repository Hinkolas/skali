<script lang="ts">
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>{data.title} · {data.service.name} — skali</title>
</svelte:head>

<EmptyState
	icon={data.icon}
	title="{data.title} isn't available yet"
	description="this page will arrive in a future version"
/>
