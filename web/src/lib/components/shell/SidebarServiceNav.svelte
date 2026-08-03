<script lang="ts">
	import { page } from '$app/state';
	import type { Project, Service, ServiceType } from '$lib/mock/types';
	import { SERVICE_TABS } from '$lib/navigation';
	import { SERVICE_KIND_META } from '$lib/service-types';
	import { currentEnv, withEnv } from '$lib/urls';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';

	let { project, service }: { project: Project; service: Service } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}/services/${service.slug}`);
	const env = $derived(currentEnv(project, page.url));

	// Active nav tint follows the service type (cyan DB sidebar per the draft).
	const activeClass: Record<ServiceType, string> = {
		application: 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav',
		database: 'bg-service-db/10 inset-ring inset-ring-service-db/25 text-[#b7e4ee]',
		cache: 'bg-service-cache/10 inset-ring inset-ring-service-cache/25 text-[#eed0e9]',
		storage: 'bg-service-storage/10 inset-ring inset-ring-service-storage/25 text-[#eedcbb]'
	};
</script>

<NavSection label={SERVICE_KIND_META[service.type].label} />
<div class="flex flex-col gap-0.5 px-2">
	{#each SERVICE_TABS[service.type] as tab (tab.slug)}
		{@const path = tab.slug ? `${base}/${tab.slug}` : base}
		<!-- Active check matches the bare path; the href carries ?env=. -->
		<NavItem
			href={withEnv(path, env)}
			label={tab.label}
			icon={tab.icon}
			active={tab.slug ? pathname.startsWith(path) : pathname === base}
			activeClass={activeClass[service.type]}
		/>
	{/each}
</div>
