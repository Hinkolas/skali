<script lang="ts">
	import { page } from '$app/state';
	import type { Project, Service, ServiceType } from '$lib/mock/types';
	import { SERVICE_TABS } from '$lib/navigation';
	import { currentEnv, withEnv } from '$lib/urls';

	let { project, service }: { project: Project; service: Service } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}/services/${service.slug}`);
	const env = $derived(currentEnv(project, page.url));

	// Active underline follows the service type, matching the row tints in
	// the sidebar's services list.
	const underlineClass: Record<ServiceType, string> = {
		application: 'bg-accent',
		database: 'bg-service-db',
		cache: 'bg-service-cache',
		storage: 'bg-service-storage'
	};
</script>

<!-- -mx-5.5 bleeds the divider to the card edges; px-2.5 plus the tabs' own
     px-3 puts the first label back on the card's 5.5 content inset. -->
<nav
	aria-label="Service"
	class="border-border-default -mx-5.5 mb-6 flex items-center gap-1 border-b px-2.5"
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
