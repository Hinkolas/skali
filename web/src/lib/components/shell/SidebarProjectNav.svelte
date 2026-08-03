<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Plus from '@lucide/svelte/icons/plus';
	import type { Project, Service, ServiceType } from '$lib/mock/types';
	import { PROJECT_TABS } from '$lib/navigation';
	import { modal } from '$lib/stores/modal.svelte';
	import StatusDot from '$lib/components/ui/StatusDot.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import NewServiceModal, {
		modalOptions as newServiceModalOptions
	} from '$lib/components/service/NewServiceModal.svelte';
	import { currentEnv, withEnv } from '$lib/urls';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';

	let { project, services }: { project: Project; services: Service[] } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}`);
	const env = $derived(currentEnv(project, page.url));

	// Active service row tint follows the service type (cyan for DBs etc.),
	// matching the underline tints in ServiceTabs.
	const serviceActiveClass: Record<ServiceType, string> = {
		application: 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav',
		database: 'bg-service-db/10 inset-ring inset-ring-service-db/25 text-[#b7e4ee]',
		cache: 'bg-service-cache/10 inset-ring inset-ring-service-cache/25 text-[#eed0e9]',
		storage: 'bg-service-storage/10 inset-ring inset-ring-service-storage/25 text-[#eedcbb]'
	};
</script>

<NavSection label="Project" />
<div class="flex flex-col gap-0.5 px-1">
	{#each PROJECT_TABS as tab (tab.slug)}
		{@const path = tab.slug ? `${base}/${tab.slug}` : base}
		<!-- Active check matches the bare path; the href carries ?env=. -->
		<NavItem
			href={withEnv(path, env)}
			label={tab.label}
			icon={tab.icon}
			active={tab.slug ? pathname.startsWith(path) : pathname === base}
		/>
	{/each}
</div>

<NavSection label="Services">
	{#snippet trailing()}
		<span class="font-mono text-accent ml-1.5 text-xs">{services.length}</span>
	{/snippet}
</NavSection>
<div class="flex flex-col gap-0.5 px-1">
	{#each services as service (service.slug)}
		{@const servicePath = `${base}/services/${service.slug}`}
		{@const active = pathname === servicePath || pathname.startsWith(servicePath + '/')}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
		<a
			href={withEnv(
				resolve('/(app)/projects/[project]/services/[service]', {
					project: project.slug,
					service: service.slug
				}),
				env
			)}
			class="flex items-center gap-2.5 rounded-[11px] px-3 py-1.75 text-lg transition-colors {active
				? `font-medium ${serviceActiveClass[service.type]}`
				: 'text-text-secondary hover:bg-white/4'}"
		>
			<TypeBadge kind={service.type} form="tile" />
			<span class="truncate">{service.name}</span>
			<span class="ml-auto flex-none">
				<StatusDot status={service.status} />
			</span>
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/each}
	<button
		type="button"
		onclick={() =>
			modal.open(NewServiceModal, { projectName: project.name }, newServiceModalOptions)}
		class="border-border-strong text-text-faint hover:text-text-secondary mt-1.5 flex cursor-pointer items-center gap-2 rounded-[11px] border border-dashed px-3 py-2 text-lg transition-colors hover:border-white/20"
	>
		<Plus size={15} />
		New service
	</button>
</div>
