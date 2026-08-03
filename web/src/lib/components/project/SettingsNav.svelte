<script lang="ts">
	import { page } from '$app/state';
	import type { Project } from '$lib/types/project';
	import { withEnv } from '$lib/urls';

	// Sub-navigation between the settings surfaces of a project.
	let { project }: { project: Project } = $props();

	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);
	const base = $derived(`/projects/${project.name}/settings`);
	const tabs = $derived([
		{ label: 'General', path: base },
		{ label: 'Values', path: `${base}/values` }
	]);
</script>

<div class="border-border-default mb-6 flex items-center gap-1 border-b">
	{#each tabs as tab (tab.path)}
		{@const active = page.url.pathname === tab.path}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path mirrors the route params, env appended by $lib/urls -->
		<a
			href={withEnv(tab.path, env)}
			class="relative px-3 py-2.5 text-lg transition-colors {active
				? 'text-text-primary font-medium'
				: 'text-text-tertiary hover:text-text-secondary'}"
		>
			{tab.label}
			{#if active}
				<span class="bg-accent absolute inset-x-3 -bottom-px h-0.5 rounded-full"></span>
			{/if}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/each}
</div>
