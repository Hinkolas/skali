<script lang="ts">
	// Shown in place of an environment's pages when the caller's effective
	// role there is none: the environment is listed by name, nothing inside
	// is readable.
	import Lock from '@lucide/svelte/icons/lock';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import type { Environment, Project } from '$lib/types/project';

	let { environment, project }: { environment: Environment; project: Project } = $props();
</script>

<svelte:head>
	<title>{project.display_name || project.name} — skali</title>
</svelte:head>

<PageHeader title={project.display_name || project.name}>
	{#snippet subtitle()}
		<span class="font-mono">{environment.name}</span> · locked
	{/snippet}
</PageHeader>

<EmptyState
	icon={Lock}
	title="This environment is locked for you"
	description="Your role on {environment.name} is none: it is listed so the name is known, but nothing inside is readable. Ask a project admin for access, or switch to another environment."
/>
