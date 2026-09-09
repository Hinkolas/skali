<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Lock from '@lucide/svelte/icons/lock';
	import Plus from '@lucide/svelte/icons/plus';
	import { CREATE_PROJECTS_TITLE, canCreateProject, requiredTitle, roleAtLeast } from '$lib/access';
	import type { OrgView } from '$lib/models/org';
	import type { ServiceView } from '$lib/models/service';
	import type { Environment, Project } from '$lib/types/project';
	import { withEnv } from '$lib/urls';
	import { modal } from '$lib/stores/modal.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import MenuSeparator from '$lib/components/ui/MenuSeparator.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';
	import NewEnvironmentModal, {
		modalOptions as newEnvironmentModalOptions
	} from '$lib/components/project/NewEnvironmentModal.svelte';

	// Same merged `page.data` contract the Sidebar reads: nested layouts set
	// `project`/`environments`/`env`/`services`/`service`; loads that
	// introduce colliding keys would break both consumers.
	const data = $derived(
		page.data as {
			user: AuthUser | null;
			org: OrgView;
			projects: Project[];
			project?: Project;
			environments?: Environment[];
			env?: Environment | null;
			services?: ServiceView[];
			service?: ServiceView;
		}
	);

	const env = $derived(data.env?.name ?? null);
	const mayCreateProject = $derived(canCreateProject(data.user));
	const mayCreateEnvironment = $derived(roleAtLeast(data.project?.access.role, 'maintain'));

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
		class="text-text-tertiary hover:text-text-primary truncate rounded-lg px-2 py-1 text-base font-medium transition-colors"
	>
		{data.org.name}
	</a>

	{#if data.project}
		{@const project = data.project}
		<span class="text-text-ghost text-md">/</span>
		<Menu label="Switch project" triggerClass="{crumbTrigger} text-text-primary">
			{#snippet trigger({ open })}
				{project.display_name || project.name}
				<ChevronDown
					size={13}
					class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
				/>
			{/snippet}
			{#each data.projects as p (p.id)}
				<MenuItem
					href={resolve('/(app)/projects/[project]', { project: p.name })}
					selected={p.id === project.id}
				>
					<span class="truncate">{p.display_name || p.name}</span>
				</MenuItem>
			{/each}
			<MenuSeparator />
			<MenuItem
				icon={Plus}
				disabled={!mayCreateProject}
				title={mayCreateProject ? undefined : CREATE_PROJECTS_TITLE}
				onselect={() => modal.open(NewProjectModal, {}, newProjectModalOptions)}
			>
				New project
			</MenuItem>
		</Menu>

		{#if env}
			<span class="text-text-ghost text-md">/</span>
			<Menu label="Switch environment" triggerClass="{crumbTrigger} text-text-secondary">
				{#snippet trigger({ open })}
					<span class="font-mono text-md">{env}</span>
					<ChevronDown
						size={13}
						class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
					/>
				{/snippet}
				{#each data.environments ?? [] as e (e.id)}
					{@const locked = e.access === 'none'}
					<MenuItem
						selected={e.name === env}
						disabled={locked}
						title={locked ? 'locked for you' : undefined}
						onselect={() => switchEnv(e.name)}
					>
						<span class="font-mono text-md">{e.name}</span>
						{#if locked}
							<Lock size={12} class="text-text-ghost ml-auto flex-none" />
						{:else if e.settings?.deploy_policy === 'promote-only' || e.settings?.priority === 'high'}
							<span class="ml-auto flex gap-1">
								{#if e.settings.deploy_policy === 'promote-only'}
									<Pill text="protected" tone="warning" />
								{/if}
								{#if e.settings.priority === 'high'}
									<Pill text="high" tone="warning" />
								{/if}
							</span>
						{/if}
					</MenuItem>
				{/each}
				<MenuSeparator />
				<MenuItem
					icon={Plus}
					disabled={!mayCreateEnvironment}
					title={mayCreateEnvironment
						? undefined
						: requiredTitle('maintain', 'project', project.name)}
					onselect={() => modal.open(NewEnvironmentModal, { project }, newEnvironmentModalOptions)}
				>
					New environment
				</MenuItem>
			</Menu>
		{/if}
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
			{#each data.services ?? [] as s (`${s.type}:${s.key}`)}
				<MenuItem
					href={withEnv(
						resolve('/(app)/projects/[project]/services/[service]', {
							project: project.name,
							service: s.key
						}),
						env
					)}
					selected={s.key === service.key && s.type === service.type}
				>
					<TypeBadge kind={s.type} form="tile" />
					<span class="truncate">{s.name}</span>
				</MenuItem>
			{/each}
		</Menu>
	{/if}
</nav>
