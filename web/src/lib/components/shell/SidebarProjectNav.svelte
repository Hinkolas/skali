<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Plus from '@lucide/svelte/icons/plus';
	import type { Project, Service } from '$lib/mock/types';
	import { PROJECT_TABS } from '$lib/navigation';
	import { modal } from '$lib/stores/modal.svelte';
	import StatusDot from '$lib/components/ui/StatusDot.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import NewServiceModal, {
		modalOptions as newServiceModalOptions
	} from '$lib/components/service/NewServiceModal.svelte';
	import BackLink from './BackLink.svelte';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';
	import SwitcherCard from './SwitcherCard.svelte';

	let { project, services }: { project: Project; services: Service[] } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}`);

	const projectDot: Record<Project['status'], string> = {
		healthy: 'bg-status-success',
		building: 'bg-status-warning',
		degraded: 'bg-status-danger'
	};
</script>

<div class="px-3">
	<BackLink href={resolve('/(app)/projects')} label="Back to all projects" />
	<SwitcherCard
		title={project.name}
		subtitle="{project.environment} · {project.service_count} services"
	>
		{#snippet leading()}
			<span class="size-2 flex-none rounded-full {projectDot[project.status]}"></span>
		{/snippet}
	</SwitcherCard>
</div>

<NavSection label="Project" />
<div class="flex flex-col gap-0.5 px-3">
	{#each PROJECT_TABS as tab (tab.slug)}
		{@const href = tab.slug ? `${base}/${tab.slug}` : base}
		<NavItem
			{href}
			label={tab.label}
			icon={tab.icon}
			active={tab.slug ? pathname.startsWith(href) : pathname === base}
		/>
	{/each}
</div>

<NavSection label="Services">
	{#snippet trailing()}
		<span class="font-mono text-accent ml-1.5 text-[10px]">{services.length}</span>
	{/snippet}
</NavSection>
<div class="flex flex-col gap-0.5 px-3">
	{#each services as service (service.slug)}
		<a
			href={resolve('/(app)/projects/[project]/services/[service]', {
				project: project.slug,
				service: service.slug
			})}
			class="text-text-secondary flex items-center gap-2.5 rounded-[10px] px-3 py-1.75 text-[13px] transition-colors hover:bg-white/4"
		>
			<TypeBadge kind={service.type} />
			<span class="truncate">{service.name}</span>
			<span class="ml-auto flex-none">
				<StatusDot status={service.status} />
			</span>
		</a>
	{/each}
	<button
		type="button"
		onclick={() =>
			modal.open(NewServiceModal, { projectName: project.name }, newServiceModalOptions)}
		class="border-border-strong text-text-faint hover:text-text-secondary mt-1.5 flex cursor-pointer items-center gap-2 rounded-[10px] border border-dashed px-3 py-2 text-[13px] transition-colors hover:border-white/20"
	>
		<Plus size={13} />
		New service
	</button>
</div>
