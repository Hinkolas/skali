<script lang="ts">
	import type { NavIcon } from '$lib/navigation';

	let {
		href,
		label,
		icon: Icon,
		active = false,
		badge,
		activeClass = 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav'
	}: {
		href: string;
		label: string;
		icon: NavIcon;
		active?: boolean;
		badge?: string | number;
		/** Override for non-violet contexts (e.g. cyan in the database sidebar). */
		activeClass?: string;
	} = $props();
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
<a
	{href}
	class="flex items-center gap-2.75 rounded-[11px] px-3 py-2 text-lg transition-colors {active
		? `font-medium ${activeClass}`
		: 'text-text-tertiary hover:bg-white/4'}"
>
	<Icon size={17} strokeWidth={1.75} class="flex-none opacity-90" />
	{label}
	{#if badge !== undefined}
		<span class="font-mono text-text-secondary ml-auto rounded-md bg-white/8 px-1.75 py-px text-xs">
			{badge}
		</span>
	{/if}
</a>
