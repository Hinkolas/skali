<script lang="ts">
	import type { ServiceStatus } from '$lib/mock/types';
	import { STATUS_META } from '$lib/service-types';

	let {
		status,
		size = 'md'
	}: {
		status: ServiceStatus;
		size?: 'sm' | 'md';
	} = $props();
</script>

<span
	class="flex-none rounded-full {size === 'sm' ? 'size-1.5' : 'size-[8px]'} {STATUS_META[status]
		.dot}"
></span>
