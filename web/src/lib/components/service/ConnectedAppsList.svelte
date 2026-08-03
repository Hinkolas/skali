<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { ServiceView } from '$lib/models/service';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import { withEnv } from '$lib/urls';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Container from '@lucide/svelte/icons/container';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	// Applications whose declared dependencies include this service.
	let { service, services }: { service: ServiceView; services: ServiceView[] } = $props();

	const projectName = $derived((page.data.project as { name: string }).name);
	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);

	const ref = $derived(`${service.type === 'database' ? 'databases' : 'buckets'}.${service.key}`);
	const apps = $derived(
		services.filter((s) => s.type === 'application' && s.dependencies.includes(ref))
	);
</script>

{#if apps.length === 0}
	<EmptyState
		icon={Container}
		title="No connected applications"
		description="declare a dependency in skali.yaml to inject connection values"
	/>
{:else}
	<Card class="overflow-hidden">
		{#each apps as app (app.key)}
			{@const meta = HEALTH_META[envStatus.service('application', app.key)?.health ?? 'unknown']}
			<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
			<a
				href={withEnv(
					resolve('/(app)/projects/[project]/services/[service]', {
						project: projectName,
						service: app.key
					}),
					env
				)}
				class="border-border-subtle flex items-center gap-3 border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2"
			>
				<TypeBadge kind="application" form="tile" />
				<span class="text-text-primary text-lg font-medium">{app.name}</span>
				<span class="font-mono text-text-faint text-xs">via {ref}</span>
				<span class="ml-auto flex items-center gap-1.5 text-md {meta.text}">
					<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label.toLowerCase()}
				</span>
			</a>
			<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{/each}
	</Card>
{/if}
