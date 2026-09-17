<script lang="ts">
	import PoolTuningForm from '$lib/components/system/PoolTuningForm.svelte';
	import type { PageData } from './$types';

	// The key remounts the form on the pool the layout reloaded after a
	// save (every PUT bumps updated_at), which re-seeds its state. A
	// writable $derived would not do: a plain record from $derived is not
	// deeply reactive, so the per-row bindings would go stale.
	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>Tuning · {data.pool.name} — skali</title>
</svelte:head>

<div class="pb-6">
	{#key data.pool.updated_at}
		<PoolTuningForm pool={data.pool} />
	{/key}
</div>
