<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Plus from '@lucide/svelte/icons/plus';
	import type { ApplicationView, ServiceView } from '$lib/models/service';
	import { withEnv } from '$lib/urls';
	import Card from '$lib/components/ui/Card.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let {
		service,
		services
	}: {
		service: ApplicationView;
		services: ServiceView[];
	} = $props();

	const projectName = $derived((page.data.project as { name: string }).name);
	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);

	// Dependencies are dotted refs like "databases.main"; map them back onto
	// the service list.
	const connected = $derived(
		service.dependencies
			.map((ref) => {
				const [collection, key] = ref.split('.', 2);
				const type = collection === 'databases' ? 'database' : 'bucket';
				return services.find((s) => s.type === type && s.key === key);
			})
			.filter((s): s is ServiceView => s !== undefined)
	);

	const hoverBorder: Record<ServiceView['type'], string> = {
		application: 'hover:border-accent/40',
		database: 'hover:border-service-db/40',
		bucket: 'hover:border-service-storage/40'
	};
</script>

<Card class="flex flex-col p-5">
	<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Connected services</h3>
	<div class="flex flex-col gap-2">
		{#each connected as target (`${target.type}:${target.key}`)}
			<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
			<a
				href={withEnv(
					resolve('/(app)/projects/[project]/services/[service]', {
						project: projectName,
						service: target.key
					}),
					env
				)}
				class="border-border-default flex items-center gap-2.5 rounded-[11px] border px-3 py-2.5 transition-colors {hoverBorder[
					target.type
				]}"
			>
				<TypeBadge kind={target.type} form="tile" />
				<span class="text-text-primary text-base font-medium">{target.name}</span>
				<span class="font-mono text-text-faint ml-auto text-2xs">
					{target.type === 'database' ? 'env injected' : 's3 keys injected'}
				</span>
			</a>
			<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{:else}
			<div class="font-mono text-text-ghost py-1 text-xs">no dependencies declared</div>
		{/each}
		<button
			type="button"
			disabled
			title="Connections are declared in skali.yaml; editing here is coming soon"
			class="border-border-strong text-text-faint flex cursor-default items-center justify-center gap-1.5 rounded-[11px] border border-dashed py-2.25 text-md opacity-60"
		>
			<Plus size={13} />
			Add connection
		</button>
	</div>
</Card>
