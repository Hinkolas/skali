<script lang="ts">
	import { resolve } from '$app/paths';
	import Plus from '@lucide/svelte/icons/plus';
	import type { ApplicationService, Service, ServiceType } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let {
		service,
		services
	}: {
		service: ApplicationService;
		services: Service[];
	} = $props();

	const connected = $derived(
		service.connected_service_slugs
			.map((slug) => services.find((s) => s.slug === slug))
			.filter((s) => s !== undefined)
	);

	const defaultPort: Record<ServiceType, string> = {
		application: ':8080',
		database: ':5432',
		cache: ':6379',
		storage: ':9000'
	};

	const hoverBorder: Record<ServiceType, string> = {
		application: 'hover:border-accent/40',
		database: 'hover:border-service-db/40',
		cache: 'hover:border-service-cache/40',
		storage: 'hover:border-service-storage/40'
	};
</script>

<Card class="flex flex-col p-5">
	<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Connected services</h3>
	<div class="flex flex-col gap-2">
		{#each connected as target (target.slug)}
			<a
				href={resolve('/(app)/projects/[project]/services/[service]', {
					project: target.project_slug,
					service: target.slug
				})}
				class="border-border-default flex items-center gap-2.5 rounded-[11px] border px-3 py-2.5 transition-colors {hoverBorder[
					target.type
				]}"
			>
				<TypeBadge kind={target.type} form="tile" />
				<span class="text-text-primary text-base font-medium">{target.name}</span>
				<span class="font-mono text-text-faint ml-auto text-2xs">
					{defaultPort[target.type]}
				</span>
			</a>
		{/each}
		<button
			type="button"
			onclick={() => toast.info('Connections are coming soon')}
			class="border-border-strong text-text-faint hover:text-text-secondary flex cursor-pointer items-center justify-center gap-1.5 rounded-[11px] border border-dashed py-2.25 text-md transition-colors hover:border-white/20"
		>
			<Plus size={13} />
			Add connection
		</button>
	</div>
</Card>
