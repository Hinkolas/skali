<script lang="ts">
	// Drives the live status store for the resolved environment. $effect runs
	// in the browser only; sync() is idempotent per environment id, so load
	// re-runs do not churn the stream.
	import { onDestroy, type Snippet } from 'svelte';
	import { page } from '$app/state';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import LockedEnvironment from '$lib/components/access/LockedEnvironment.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: Snippet } = $props();

	// A locked environment has no readable contents: the operational pages
	// show the lock instead; the settings pages stay (project-level controls
	// and the environment list are still the caller's to see).
	const locked = $derived(data.env?.access === 'none' && !page.url.pathname.includes('/settings'));

	$effect(() => {
		if (data.env && data.env.access !== 'none') envStatus.sync(data.env.id, data.status);
		else envStatus.stop();
	});
	onDestroy(() => envStatus.stop());
</script>

{#if locked && data.env}
	<LockedEnvironment environment={data.env} project={data.project} />
{:else}
	{@render children()}
{/if}
