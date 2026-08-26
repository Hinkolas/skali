<script lang="ts">
	import { HEALTH_META } from '$lib/service-types';
	import type { ServiceHealth } from '$lib/types/project';

	// Service health from the status projection (unknown/progressing/
	// healthy/degraded/unhealthy).
	let {
		status,
		size = 'md'
	}: {
		status: ServiceHealth;
		size?: 'sm' | 'md';
	} = $props();

	const meta = $derived(HEALTH_META[status]);
</script>

<span class="flex-none rounded-full {size === 'sm' ? 'size-1.5' : 'size-[8px]'} {meta.dot}"></span>
