<script lang="ts">
	import type { Snippet } from 'svelte';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';

	let {
		variant = 'secondary',
		size = 'md',
		href,
		type = 'button',
		disabled = false,
		busy = false,
		title,
		class: className = '',
		onclick,
		children
	}: {
		variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
		size?: 'sm' | 'md';
		href?: string;
		type?: 'button' | 'submit';
		disabled?: boolean;
		busy?: boolean;
		/** Tooltip; on a disabled button it carries the explanation. */
		title?: string;
		class?: string;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();

	const variantClass: Record<string, string> = {
		primary:
			'bg-linear-135 from-accent-from to-accent-to font-semibold text-surface-base transition-[filter] hover:brightness-108 active:brightness-95',
		secondary:
			'border border-border-strong bg-white/2 font-medium text-text-secondary transition-colors hover:bg-white/5 active:bg-white/7',
		ghost:
			'font-medium text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary active:bg-white/7',
		danger:
			'bg-status-danger font-semibold text-white transition-[filter] hover:brightness-108 active:brightness-95'
	};

	const sizeClass: Record<string, string> = {
		sm: 'rounded-[8px] px-2.5 py-1 text-md',
		md: 'rounded-[11px] px-4.5 py-2.25 text-lg'
	};

	// The violet halo bleeds ~18px past the box, so a glowing button reads
	// larger than it is. That weight suits full-size primaries in page
	// bodies; small primaries sit in dense chrome (the topbar) where the
	// halo makes them look oversized next to unglowing neighbors.
	const glow = $derived(variant === 'primary' && size === 'md' ? 'shadow-glow' : '');

	const classes = $derived(
		`inline-flex cursor-pointer items-center justify-center gap-2 whitespace-nowrap duration-150 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent/70 disabled:cursor-default disabled:opacity-60 ${variantClass[variant]} ${sizeClass[size]} ${glow} ${className}`
	);
</script>

{#if href}
	<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
	<a {href} {title} class={classes} {onclick}>
		{@render children()}
	</a>
{:else}
	<button {type} disabled={disabled || busy} {title} class={classes} {onclick}>
		{#if busy}
			<LoaderCircle class="size-4 animate-spin" />
		{/if}
		{@render children()}
	</button>
{/if}
