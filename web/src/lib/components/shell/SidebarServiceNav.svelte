<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { Project, Service, ServiceType } from '$lib/mock/types';
	import { SERVICE_TABS } from '$lib/navigation';
	import { SERVICE_KIND_META } from '$lib/service-types';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import BackLink from './BackLink.svelte';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';
	import SwitcherCard from './SwitcherCard.svelte';

	let { project, service }: { project: Project; service: Service } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}/services/${service.slug}`);

	// Active nav tint follows the service type (cyan DB sidebar per the draft).
	const activeClass: Record<ServiceType, string> = {
		application: 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav',
		database: 'bg-service-db/10 inset-ring inset-ring-service-db/25 text-[#b7e4ee]',
		cache: 'bg-service-cache/10 inset-ring inset-ring-service-cache/25 text-[#eed0e9]',
		storage: 'bg-service-storage/10 inset-ring inset-ring-service-storage/25 text-[#eedcbb]'
	};
</script>

<div class="px-3">
	<BackLink
		href={resolve('/(app)/projects/[project]', { project: project.slug })}
		label="Back to {project.name}"
	/>
	<SwitcherCard title={service.name} subtitle={service.kind_label}>
		{#snippet leading()}
			<TypeBadge kind={service.type} form="tile" />
		{/snippet}
	</SwitcherCard>
</div>

<NavSection label={SERVICE_KIND_META[service.type].label} />
<div class="flex flex-col gap-0.5 px-3">
	{#each SERVICE_TABS[service.type] as tab (tab.slug)}
		{@const href = tab.slug ? `${base}/${tab.slug}` : base}
		<NavItem
			{href}
			label={tab.label}
			icon={tab.icon}
			active={tab.slug ? pathname.startsWith(href) : pathname === base}
			activeClass={activeClass[service.type]}
		/>
	{/each}
</div>
