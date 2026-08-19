<script lang="ts">
	import { page } from '$app/state';
	import { roleAtLeast } from '$lib/access';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import SettingsNav from '$lib/components/project/SettingsNav.svelte';
	import MembersGrid from '$lib/components/access/MembersGrid.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const user = $derived(page.data.user as AuthUser | null);
	const canEdit = $derived(roleAtLeast(data.project.access.role, 'admin'));
</script>

<svelte:head>
	<title>Members · {data.project.display_name || data.project.name} — skali</title>
</svelte:head>

<PageHeader title="Settings">
	{#snippet subtitle()}
		{data.project.name} · who may do what
	{/snippet}
</PageHeader>

<SettingsNav project={data.project} />

<div class="flex flex-col gap-3.5 pb-6">
	<MembersGrid
		project={data.project}
		environments={data.environments}
		members={data.members}
		{canEdit}
		self={user}
	/>
</div>
