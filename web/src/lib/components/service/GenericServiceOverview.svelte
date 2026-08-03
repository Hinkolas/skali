<script lang="ts">
	import Gauge from '@lucide/svelte/icons/gauge';
	import type { CacheService, StatCardData, StorageService } from '$lib/mock/types';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';

	let { service }: { service: CacheService | StorageService } = $props();

	const stats = $derived.by((): StatCardData[] => {
		if (service.type === 'cache') {
			return [
				{ label: 'HIT RATE', value: service.hit_rate },
				{ label: 'KEYS', value: service.keys },
				{
					label: 'MEMORY',
					value: service.mem_used,
					unit: `/ ${service.mem_total}`
				},
				{ label: 'ENDPOINT', value: '', note: service.endpoint }
			];
		}
		return [
			{ label: 'SIZE', value: service.size },
			{ label: 'OBJECTS', value: service.objects },
			{ label: 'EGRESS', value: service.egress_per_day },
			{ label: 'NODES', value: '', note: service.node }
		];
	});
</script>

<div class="mb-6 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<EmptyState
	icon={Gauge}
	title="Detailed view coming soon"
	description="this service type only has a summary in the prototype"
/>
