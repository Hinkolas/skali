<script lang="ts" module>
	import type { NavIcon } from '$lib/navigation';

	export type SubNavTab = {
		label: string;
		/** The tab's path without the env param. */
		path: string;
		icon: NavIcon;
		/** Active only on the exact path (an index tab whose siblings nest under it). */
		exact?: boolean;
	};
</script>

<script lang="ts">
	import { page } from '$app/state';
	import { withEnv } from '$lib/urls';

	// In-page sub-navigation between the routes of one entity (a project's
	// settings surfaces, a database pool's tabs): an underlined tab bar
	// whose tabs are links, so each tab is its own route.
	let {
		label,
		tabs,
		env = null
	}: {
		/** aria-label of the nav. */
		label: string;
		tabs: SubNavTab[];
		/** Environment name appended as `?env=`, when the pages are environment-scoped. */
		env?: string | null;
	} = $props();

	const pathname = $derived(page.url.pathname);
	function isActive(tab: SubNavTab): boolean {
		return tab.exact ? pathname === tab.path : pathname.startsWith(tab.path);
	}
</script>

<!-- -mx-4 bleeds the divider across the main pane's padding; px-1 plus the
     tabs' own px-3 puts the first label back on the content inset. On a
     narrow pane the bar scrolls sideways instead of clipping the last tabs. -->
<nav
	aria-label={label}
	class="border-border-default -mx-4 mb-6 flex items-center gap-1 overflow-x-auto overflow-y-hidden border-b px-1 [scrollbar-width:none]"
>
	{#each tabs as tab (tab.path)}
		{@const active = isActive(tab)}
		{@const Icon = tab.icon}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path mirrors the route params, env appended by $lib/urls -->
		<a
			href={withEnv(tab.path, env)}
			aria-current={active ? 'page' : undefined}
			class="relative flex flex-none items-center gap-2 px-3 py-2.5 text-lg transition-colors {active
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
