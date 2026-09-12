<script lang="ts">
	import { page } from '$app/state';
	import type { ServiceView } from '$lib/models/service';
	import type { Project } from '$lib/types/project';
	import { SERVICE_TABS } from '$lib/navigation';
	import { withEnv } from '$lib/urls';

	let { project, service }: { project: Project; service: ServiceView } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.name}/services/${service.key}`);
	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);

	// Active underline follows the service type, matching the row tints in
	// the sidebar's services list.
	const underlineClass: Record<ServiceView['type'], string> = {
		application: 'bg-accent',
		database: 'bg-service-db',
		bucket: 'bg-service-storage'
	};
</script>

<!-- -mx-4 bleeds the divider across the main pane's padding (up to its
     scrollbar gutters); px-1 plus the tabs' own px-3 puts the first label
     back on the pane's px-4 content inset. -->
<nav
	aria-label="Service"
	class="border-border-default -mx-4 mb-6 flex items-center gap-1 border-b px-1"
>
	{#each SERVICE_TABS[service.type] as tab (tab.slug)}
		{@const path = tab.slug ? `${base}/${tab.slug}` : base}
		{@const active = tab.slug ? pathname.startsWith(path) : pathname === base}
		{@const Icon = tab.icon}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path mirrors the route params, env appended by $lib/urls -->
		<a
			href={withEnv(path, env)}
			class="relative flex items-center gap-2 px-3 py-2.5 text-lg transition-colors {active
				? 'text-text-primary font-medium'
				: 'text-text-tertiary hover:text-text-secondary'}"
		>
			<Icon size={15} strokeWidth={1.75} class="flex-none opacity-90" />
			{tab.label}
			{#if active}
				<span
					class="absolute inset-x-3 -bottom-px h-0.5 rounded-full {underlineClass[service.type]}"
				></span>
			{/if}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/each}
</nav>
