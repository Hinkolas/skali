<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import { CREATE_PROJECTS_TITLE, canCreateProject } from '$lib/access';
	import { modal } from '$lib/stores/modal.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import FolderKanban from '@lucide/svelte/icons/folder-kanban';
	import ProjectCard from '$lib/components/project/ProjectCard.svelte';
	import NewProjectModal, {
		modalOptions as newProjectModalOptions
	} from '$lib/components/project/NewProjectModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const mayCreate = $derived(canCreateProject(data.user));
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
	<div class="grid grid-cols-3 gap-3.5 pb-6">
		{#each data.projects as project (project.id)}
			<ProjectCard {project} />
		{/each}
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
