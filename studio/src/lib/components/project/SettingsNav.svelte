<script lang="ts">
	import { page } from '$app/state';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import Users from '@lucide/svelte/icons/users';
	import type { Project } from '$lib/types/project';
	import SubNav, { type SubNavTab } from '$lib/components/ui/SubNav.svelte';

	// Sub-navigation between the settings surfaces of a project, drawn like
	// the service tab bar so the two read as the same control.
	let { project }: { project: Project } = $props();

	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);
	const base = $derived(`/projects/${project.name}/settings`);
	const tabs = $derived<SubNavTab[]>([
		{ label: 'General', path: base, icon: SlidersHorizontal, exact: true },
		{ label: 'Values', path: `${base}/values`, icon: KeyRound },
		{ label: 'Members', path: `${base}/members`, icon: Users }
	]);
</script>

<SubNav label="Settings" {tabs} {env} />
