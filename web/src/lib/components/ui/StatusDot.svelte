<script lang="ts">
	import { HEALTH_META, STATUS_META, type ServiceStatus } from '$lib/service-types';
	import type { ServiceHealth } from '$lib/types/project';

	// Accepts both vocabularies: real service health (unknown/progressing/
	// healthy/degraded/unhealthy) and the legacy graph statuses.
	let {
		status,
		size = 'md'
	}: {
		status: ServiceStatus | ServiceHealth;
		size?: 'sm' | 'md';
	} = $props();

	const meta = $derived(
		status in STATUS_META
			? STATUS_META[status as ServiceStatus]
			: HEALTH_META[status as ServiceHealth]
	);
</script>

<span class="flex-none rounded-full {size === 'sm' ? 'size-1.5' : 'size-[8px]'} {meta.dot}"></span>
