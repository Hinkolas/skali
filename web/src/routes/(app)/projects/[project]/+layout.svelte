<script lang="ts">
	// Drives the live status store for the resolved environment. $effect runs
	// in the browser only; sync() is idempotent per environment id, so load
	// re-runs do not churn the stream.
	import { onDestroy, type Snippet } from 'svelte';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: Snippet } = $props();

	$effect(() => {
		if (data.env) envStatus.sync(data.env.id, data.status);
		else envStatus.stop();
	});
	onDestroy(() => envStatus.stop());
</script>

{@render children()}
