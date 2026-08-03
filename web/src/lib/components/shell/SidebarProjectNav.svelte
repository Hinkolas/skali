<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Plus from '@lucide/svelte/icons/plus';
	import type { ServiceView } from '$lib/models/service';
	import type { Project } from '$lib/types/project';
	import { PROJECT_TABS } from '$lib/navigation';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import StatusDot from '$lib/components/ui/StatusDot.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import { withEnv } from '$lib/urls';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';

	let { project, services }: { project: Project; services: ServiceView[] } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.name}`);
	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);

	// Active service row tint follows the service type (cyan for DBs etc.),
	// matching the underline tints in ServiceTabs.
	const serviceActiveClass: Record<ServiceView['type'], string> = {
		application: 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav',
		database: 'bg-service-db/10 inset-ring inset-ring-service-db/25 text-[#b7e4ee]',
		bucket: 'bg-service-storage/10 inset-ring inset-ring-service-storage/25 text-[#eedcbb]'
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
	{#each services as service (`${service.type}:${service.key}`)}
		{@const servicePath = `${base}/services/${service.key}`}
		{@const active = pathname === servicePath || pathname.startsWith(servicePath + '/')}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
		<a
			href={withEnv(
				resolve('/(app)/projects/[project]/services/[service]', {
					project: project.name,
					service: service.key
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
				<StatusDot status={envStatus.service(service.type, service.key)?.health ?? 'unknown'} />
			</span>
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/each}
	<button
		type="button"
		disabled
		title="Services are defined in skali.yaml; adding them here is coming soon"
		class="border-border-strong text-text-faint mt-1.5 flex cursor-default items-center gap-2 rounded-[11px] border border-dashed px-3 py-2 text-lg opacity-60"
	>
		<Plus size={15} />
		New service
	</button>
</div>
