<script lang="ts">
	import { page } from '$app/state';
	import { ORG_NAV } from '$lib/navigation';
	import NavItem from './NavItem.svelte';
	import NavSection from './NavSection.svelte';

	const pathname = $derived(page.url.pathname);

	// page.data.user comes from the (app) server layout, so admin-only items
	// are resolved during SSR too (no post-hydration pop-in).
	const isAdmin = $derived(page.data.user?.role === 'admin');
	const groups = $derived(
		ORG_NAV.map((group) => ({
			...group,
			items: group.items.filter((item) => !item.adminOnly || isAdmin)
		})).filter((group) => group.items.length > 0)
	);
</script>

{#each groups as group (group.section)}
	<NavSection label={group.section} />
	<div class="flex flex-col gap-0.5 px-1">
		{#each group.items as item (item.slug)}
			{@const href = `/${item.slug}`}
			<NavItem {href} label={item.label} icon={item.icon} active={pathname === href} />
		{/each}
	</div>
{/each}
