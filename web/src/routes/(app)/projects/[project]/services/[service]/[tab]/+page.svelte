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
	title="{data.title} isn't part of the prototype yet"
	description="this page will land in a later design pass"
/>
