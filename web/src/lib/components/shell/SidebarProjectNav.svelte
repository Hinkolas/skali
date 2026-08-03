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
	import { currentEnv, withEnv } from '$lib/urls';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';

	let { project, services }: { project: Project; services: Service[] } = $props();

	const pathname = $derived(page.url.pathname);
	const base = $derived(`/projects/${project.slug}`);
	const env = $derived(currentEnv(project, page.url));
</script>

<NavSection label="Project" />
<div class="flex flex-col gap-0.5 px-2">
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
		<span class="font-mono text-accent ml-1.5 text-[10px]">{services.length}</span>
	{/snippet}
</NavSection>
<div class="flex flex-col gap-0.5 px-2">
	{#each services as service (service.slug)}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
		<a
			href={withEnv(
				resolve('/(app)/projects/[project]/services/[service]', {
					project: project.slug,
					service: service.slug
				}),
				env
			)}
			class="text-text-secondary flex items-center gap-2.5 rounded-[10px] px-3 py-1.75 text-[13px] transition-colors hover:bg-white/4"
		>
			<TypeBadge kind={service.type} />
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
		class="border-border-strong text-text-faint hover:text-text-secondary mt-1.5 flex cursor-pointer items-center gap-2 rounded-[10px] border border-dashed px-3 py-2 text-[13px] transition-colors hover:border-white/20"
	>
		<Plus size={13} />
		New service
	</button>
</div>
