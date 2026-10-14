<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ArrowUpDown from '@lucide/svelte/icons/arrow-up-down';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import FolderKanban from '@lucide/svelte/icons/folder-kanban';
	import LayoutGrid from '@lucide/svelte/icons/layout-grid';
	import List from '@lucide/svelte/icons/list';
	import Plus from '@lucide/svelte/icons/plus';
	import { CREATE_PROJECTS_TITLE, canCreateProject, isInstanceAdmin } from '$lib/access';
	import { deferred } from '$lib/deferred.svelte';
	import { PROJECT_SORTS, sortProjects, type ProjectSort } from '$lib/models/project';
	import { readPref, writePref } from '$lib/prefs';
	import { modal } from '$lib/stores/modal.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import Segmented from '$lib/components/ui/Segmented.svelte';
	import ProjectCard from '$lib/components/project/ProjectCard.svelte';
	import ProjectList from '$lib/components/project/ProjectList.svelte';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';
	import type { PageData } from './$types';

	// data.projects is the shell's plain list; data.summary is this page's
	// own request for the same projects with summaries, returned unawaited
	// (see +page.ts). The page paints from the plain list right away and
	// swaps in the summaries when they settle; a reload keeps the previous
	// summaries on screen instead of flashing skeletons.
	let { data }: { data: PageData } = $props();

	const summary = deferred(() => data.summary);
	const source = $derived(summary.value ?? data.projects);
	const loading = $derived(summary.pending && summary.value === null);

	// A project being deleted leaves the list once its last environment is
	// gone. Nothing streams the list itself, so it re-reads while a delete
	// is going and stops when none is.
	const DELETING_REFRESH_MS = 5000;
	const anyDeleting = $derived(source.some((p) => p.deleting_at));
	$effect(() => {
		if (!anyDeleting) return;
		const timer = setInterval(() => void invalidateAll(), DELETING_REFRESH_MS);
		return () => clearInterval(timer);
	});

	const mayCreate = $derived(canCreateProject(data.user));

	// The toolbar choices are per-browser preferences: a crowded cluster
	// wants the list every time, an instance admin who administers projects
	// without belonging to them wants their own by default, and whoever
	// watches rollouts wants the latest update on top.
	const VIEWS = ['cards', 'list'] as const;
	const SCOPES = ['all', 'mine'] as const;
	let view = $state(readPref('skali.projects.view', VIEWS, 'cards'));
	let scope = $state(readPref('skali.projects.scope', SCOPES, 'all'));
	let sort = $state(readPref('skali.projects.sort', PROJECT_SORTS, 'name-asc'));

	const SORT_LABELS: Record<ProjectSort, string> = {
		'name-asc': 'Name, A to Z',
		'name-desc': 'Name, Z to A',
		'updated-desc': 'Updated, newest first',
		'updated-asc': 'Updated, oldest first'
	};

	// The scope only exists for instance admins: everyone else sees their
	// memberships and nothing more, so "mine" would equal "all".
	const scoped = $derived(isInstanceAdmin(data.user));
	const mine = $derived(source.filter((p) => p.access.member));
	const projects = $derived(sortProjects(scoped && scope === 'mine' ? mine : source, sort));

	function setView(id: string) {
		view = id as (typeof VIEWS)[number];
		writePref('skali.projects.view', view);
	}
	function setScope(id: string) {
		scope = id as (typeof SCOPES)[number];
		writePref('skali.projects.scope', scope);
	}
	function setSort(id: ProjectSort) {
		sort = id;
		writePref('skali.projects.sort', sort);
	}
</script>

<svelte:head>
	<title>Projects — skali</title>
</svelte:head>

<PageHeader title="Projects">
	{#snippet subtitle()}
		{data.org.project_count} project{data.org.project_count === 1 ? '' : 's'}
		{#if data.org.node_count !== null}
			across {data.org.node_count} node{data.org.node_count === 1 ? '' : 's'}
		{/if}
	{/snippet}
	{#snippet actions()}
		<Button
			variant="primary"
			disabled={!mayCreate}
			title={mayCreate ? undefined : CREATE_PROJECTS_TITLE}
			onclick={() => modal.open(NewProjectModal, {}, newProjectModalOptions)}
		>
			<Plus size={17} strokeWidth={2.5} />
			New project
		</Button>
	{/snippet}
</PageHeader>

{#if data.projects.length > 0}
	<div class="mb-4 flex flex-wrap items-center gap-3">
		{#if scoped}
			<Segmented
				label="Which projects to show"
				value={scope}
				onchange={setScope}
				segments={[
					{
						id: 'all',
						label: 'All',
						count: data.projects.length,
						title: 'every project on this instance'
					},
					{
						id: 'mine',
						label: 'My projects',
						count: mine.length,
						title: 'projects you hold a membership on'
					}
				]}
			/>
		{/if}
		<div class="ml-auto flex items-center gap-2">
			<Menu
				label="Sort projects"
				align="end"
				panelClass="min-w-56"
				triggerClass="flex h-8 cursor-pointer items-center gap-1.5 rounded-[11px] bg-white/3 px-2.5 text-sm text-text-faint transition-colors hover:bg-white/5 hover:text-text-secondary"
			>
				{#snippet trigger()}
					<ArrowUpDown size={14} strokeWidth={1.75} class="flex-none opacity-90" />
					<span class="sr-only">Sort by</span>
					{SORT_LABELS[sort]}
					<ChevronDown size={13} class="flex-none opacity-70" />
				{/snippet}
				{#each PROJECT_SORTS as id (id)}
					<MenuItem selected={id === sort} onselect={() => setSort(id)}>
						{SORT_LABELS[id]}
					</MenuItem>
				{/each}
			</Menu>
			<Segmented
				label="View"
				value={view}
				onchange={setView}
				segments={[
					{ id: 'cards', icon: LayoutGrid, title: 'Cards' },
					{ id: 'list', icon: List, title: 'List' }
				]}
			/>
		</div>
	</div>
{/if}

{#if projects.length > 0}
	{#if view === 'list'}
		<div class="pb-6">
			<ProjectList {projects} {loading} />
		</div>
	{:else}
		<div
			class="grid grid-cols-1 gap-3.5 pb-6 @2xl:grid-cols-2 @5xl:grid-cols-3"
			aria-busy={loading}
		>
			{#each projects as project (project.id)}
				<ProjectCard {project} {loading} />
			{/each}
		</div>
	{/if}
{:else if data.projects.length > 0}
	<div class="pb-6">
		<EmptyState
			icon={FolderKanban}
			title="No memberships yet"
			description="You see every project as an instance admin, but none lists you as a member. Switch to All to see them."
		/>
	</div>
{:else}
	<div class="pb-6">
		<EmptyState
			icon={FolderKanban}
			title={mayCreate ? 'No projects yet' : 'No projects you can see'}
			description={mayCreate
				? 'Create a project here or run skali deploy in a repository.'
				: 'Projects appear once a project admin grants you a role on them.'}
		/>
	</div>
{/if}
