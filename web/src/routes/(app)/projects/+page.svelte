<script lang="ts">
	import FolderKanban from '@lucide/svelte/icons/folder-kanban';
	import LayoutGrid from '@lucide/svelte/icons/layout-grid';
	import List from '@lucide/svelte/icons/list';
	import Plus from '@lucide/svelte/icons/plus';
	import { CREATE_PROJECTS_TITLE, canCreateProject, isInstanceAdmin } from '$lib/access';
	import { readPref, writePref } from '$lib/prefs';
	import { modal } from '$lib/stores/modal.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Segmented from '$lib/components/ui/Segmented.svelte';
	import ProjectCard from '$lib/components/project/ProjectCard.svelte';
	import ProjectList from '$lib/components/project/ProjectList.svelte';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const mayCreate = $derived(canCreateProject(data.user));

	// Both toolbar choices are per-browser preferences: a crowded cluster
	// wants the list every time, and an instance admin who administers
	// projects without belonging to them wants their own by default.
	const VIEWS = ['cards', 'list'] as const;
	const SCOPES = ['all', 'mine'] as const;
	let view = $state(readPref('skali.projects.view', VIEWS, 'cards'));
	let scope = $state(readPref('skali.projects.scope', SCOPES, 'all'));

	// The scope only exists for instance admins: everyone else sees their
	// memberships and nothing more, so "mine" would equal "all".
	const scoped = $derived(isInstanceAdmin(data.user));
	const mine = $derived(data.projects.filter((p) => p.access.member));
	const projects = $derived(scoped && scope === 'mine' ? mine : data.projects);

	function setView(id: string) {
		view = id as (typeof VIEWS)[number];
		writePref('skali.projects.view', view);
	}
	function setScope(id: string) {
		scope = id as (typeof SCOPES)[number];
		writePref('skali.projects.scope', scope);
	}
</script>

<svelte:head>
	<title>Projects — skali</title>
</svelte:head>

<PageHeader title="Projects">
	{#snippet subtitle()}
		{data.org.project_count} project{data.org.project_count === 1 ? '' : 's'} across
		{data.org.node_count} node{data.org.node_count === 1 ? '' : 's'}
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
		<div class="ml-auto">
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
			<ProjectList {projects} />
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-3.5 pb-6 @2xl:grid-cols-2 @5xl:grid-cols-3">
			{#each projects as project (project.id)}
				<ProjectCard {project} />
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
