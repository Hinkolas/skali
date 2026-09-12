<script lang="ts">
	import { page } from '$app/state';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import Users from '@lucide/svelte/icons/users';
	import type { Project } from '$lib/types/project';
	import { withEnv } from '$lib/urls';

	// Sub-navigation between the settings surfaces of a project, drawn like
	// the service tab bar so the two read as the same control.
	let { project }: { project: Project } = $props();

	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);
	const base = $derived(`/projects/${project.name}/settings`);
	const tabs = $derived([
		{ label: 'General', path: base, icon: SlidersHorizontal },
		{ label: 'Values', path: `${base}/values`, icon: KeyRound },
		{ label: 'Members', path: `${base}/members`, icon: Users }
	]);
</script>

<!-- -mx-4 bleeds the divider across the main pane's padding; px-1 plus the
     tabs' own px-3 puts the first label back on the content inset. -->
<nav
	aria-label="Settings"
	class="border-border-default -mx-4 mb-6 flex items-center gap-1 border-b px-1"
>
	{#each tabs as tab (tab.path)}
		{@const active = page.url.pathname === tab.path}
		{@const Icon = tab.icon}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path mirrors the route params, env appended by $lib/urls -->
		<a
			href={withEnv(tab.path, env)}
			class="relative flex items-center gap-2 px-3 py-2.5 text-lg transition-colors {active
				? 'text-text-primary font-medium'
				: 'text-text-tertiary hover:text-text-secondary'}"
		>
			<Icon size={15} strokeWidth={1.75} class="flex-none opacity-90" />
			{tab.label}
			{#if active}
				<span class="bg-accent absolute inset-x-3 -bottom-px h-0.5 rounded-full"></span>
			{/if}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/each}
</nav>
