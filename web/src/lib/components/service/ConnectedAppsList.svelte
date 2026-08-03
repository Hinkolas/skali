<script lang="ts">
	import { resolve } from '$app/paths';
	import type { DatabaseService } from '$lib/mock/types';
	import Card from '$lib/components/ui/Card.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { service }: { service: DatabaseService } = $props();
</script>

<Card class="overflow-hidden">
	{#each service.connected_apps as app (app.slug)}
		<a
			href={resolve('/(app)/projects/[project]/services/[service]', {
				project: service.project_slug,
				service: app.slug
			})}
			class="border-border-subtle flex items-center gap-3 border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2"
		>
			<TypeBadge kind="application" form="tile" />
			<span class="text-text-primary text-lg font-medium">{app.name}</span>
			<span class="font-mono text-text-faint text-xs">env {app.env_var}</span>
			<span class="text-status-success ml-auto flex items-center gap-1.5 text-md">
				<span class="bg-status-success size-[8px] rounded-full"></span>connected
			</span>
		</a>
	{/each}
</Card>
