<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Plus from '@lucide/svelte/icons/plus';
	import type { Org, Project, Service } from '$lib/mock/types';
	import { currentEnv, withEnv } from '$lib/urls';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import MenuSeparator from '$lib/components/ui/MenuSeparator.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';

	// Same merged `page.data` contract the Sidebar reads: nested layouts set
	// `project`/`services`/`service`; loads that introduce colliding keys
	// would break both consumers.
	const data = $derived(
		page.data as {
			org: Org;
			projects: Project[];
			project?: Project;
			services?: Service[];
			service?: Service;
		}
	);

	const env = $derived(currentEnv(data.project, page.url));

	const crumbTrigger =
		'flex cursor-pointer items-center gap-1 rounded-lg px-2 py-1 text-base font-medium transition-colors hover:bg-white/4';

	function switchEnv(name: string) {
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- same pathname, env param rewritten in place
		void goto(withEnv(page.url.pathname, name), { noScroll: true, keepFocus: true });
	}
</script>

<!-- -ml-2 cancels the first crumb's padding so its text aligns flush with
     the main card's left edge below. -->
<nav aria-label="Breadcrumbs" class="-ml-2 flex min-w-0 items-center gap-1">
	<a
		href={resolve('/(app)/projects')}
		class="text-text-tertiary hover:text-text-primary rounded-lg px-2 py-1 text-base font-medium transition-colors"
	>
		{data.org.name}
	</a>

	{#if data.project}
		{@const project = data.project}
		<span class="text-text-ghost text-md">/</span>
		<Menu label="Switch project" triggerClass="{crumbTrigger} text-text-primary">
			{#snippet trigger({ open })}
				{project.name}
				<ChevronDown
					size={13}
					class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
				/>
			{/snippet}
			{#each data.projects as p (p.slug)}
				<MenuItem
					href={resolve('/(app)/projects/[project]', { project: p.slug })}
					selected={p.slug === project.slug}
				>
					<span class="truncate">{p.name}</span>
				</MenuItem>
			{/each}
			<MenuSeparator />
			<MenuItem
				icon={Plus}
				onselect={() => modal.open(NewProjectModal, {}, newProjectModalOptions)}
			>
				New project
			</MenuItem>
		</Menu>

		<span class="text-text-ghost text-md">/</span>
		<Menu label="Switch environment" triggerClass="{crumbTrigger} text-text-secondary">
			{#snippet trigger({ open })}
				<span class="font-mono text-md">{env}</span>
				<ChevronDown
					size={13}
					class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
				/>
			{/snippet}
			{#each project.environments as e (e.name)}
				<MenuItem selected={e.name === env} onselect={() => switchEnv(e.name)}>
					<span class="font-mono text-md">{e.name}</span>
				</MenuItem>
			{/each}
			<MenuSeparator />
			<MenuItem icon={Plus} onselect={() => toast.info('Environments are coming soon')}>
				New environment
			</MenuItem>
		</Menu>
	{/if}

	{#if data.project && data.service}
		{@const project = data.project}
		{@const service = data.service}
		<span class="text-text-ghost text-md">/</span>
		<Menu label="Switch service" triggerClass="{crumbTrigger} text-text-primary">
			{#snippet trigger({ open })}
				{service.name}
				<ChevronDown
					size={13}
					class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
				/>
			{/snippet}
			{#each data.services ?? [] as s (s.slug)}
				<MenuItem
					href={withEnv(
						resolve('/(app)/projects/[project]/services/[service]', {
							project: project.slug,
							service: s.slug
						}),
						env
					)}
					selected={s.slug === service.slug}
				>
					<TypeBadge kind={s.type} form="tile" />
					<span class="truncate">{s.name}</span>
				</MenuItem>
			{/each}
		</Menu>
	{/if}
</nav>
