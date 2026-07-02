<script lang="ts">
	import { page } from '$app/state';
	import type { Org } from '$lib/mock/types';
	import { ORG_NAV } from '$lib/navigation';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';
	import SwitcherCard from './SwitcherCard.svelte';

	let { org }: { org: Org } = $props();

	const pathname = $derived(page.url.pathname);
</script>

<div class="px-3">
	<SwitcherCard title={org.name} subtitle="{org.project_count} projects · {org.node_count} nodes">
		{#snippet leading()}
			<span
				class="font-mono bg-accent/15 text-accent-light grid size-6.5 flex-none place-items-center rounded-lg text-[10px]"
			>
				{org.slug.slice(0, 2)}
			</span>
		{/snippet}
	</SwitcherCard>
</div>

{#each ORG_NAV as group (group.section)}
	<NavSection label={group.section} />
	<div class="flex flex-col gap-0.5 px-3">
		{#each group.items as item (item.slug)}
			{@const href = `/${item.slug}`}
			<NavItem
				{href}
				label={item.label}
				icon={item.icon}
				active={pathname === href}
				badge={item.slug === 'alerts' && org.alert_count > 0 ? org.alert_count : undefined}
			/>
		{/each}
	</div>
{/each}
