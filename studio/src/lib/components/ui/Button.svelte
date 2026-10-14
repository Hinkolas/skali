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
		ariaLabel,
		class: className = '',
		onclick,
		children
	}: {
		variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
		/** icon: a square the height of sm, for a lone glyph. */
		size?: 'sm' | 'md' | 'lg' | 'icon';
		href?: string;
		type?: 'button' | 'submit';
		disabled?: boolean;
		busy?: boolean;
		/** Tooltip; on a disabled button it carries the explanation. */
		title?: string;
		/** Accessible name for icon-only buttons, whose visible content is not text. */
		ariaLabel?: string;
		class?: string;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();

	const variantClass: Record<string, string> = {
		primary:
			'bg-accent-fill font-medium text-white transition-[filter] hover:brightness-108 active:brightness-95',
		secondary:
			'border border-border-strong bg-white/2 font-medium text-text-secondary transition-colors hover:bg-white/5 active:bg-white/7',
		ghost:
			'font-medium text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary active:bg-white/7',
		danger:
			'bg-status-danger font-medium text-white transition-[filter] hover:brightness-108 active:brightness-95'
	};

	const sizeClass: Record<string, string> = {
		sm: 'rounded-[8px] px-2.5 py-1 text-md',
		icon: 'size-7 rounded-[8px] text-md',
		// The height of the topbar controls, so page-header actions line up
		// with Actions and Search instead of towering over them.
		md: 'h-8 rounded-[11px] px-3.5 text-base',
		// Full-width submits in auth forms: the height of the inputs above them.
		lg: 'rounded-[11px] px-4.5 py-2.75 text-lg'
	};

	const classes = $derived(
		`inline-flex cursor-pointer items-center justify-center gap-2 whitespace-nowrap duration-150 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent/70 disabled:cursor-default disabled:opacity-60 ${variantClass[variant]} ${sizeClass[size]} ${className}`
	);
</script>

{#if href}
	<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
	<a {href} {title} aria-label={ariaLabel} class={classes} {onclick}>
		{@render children()}
	</a>
{:else}
	<button
		{type}
		disabled={disabled || busy}
		{title}
		aria-label={ariaLabel}
		class={classes}
		{onclick}
	>
		{#if busy}
			<LoaderCircle class="size-4 animate-spin" />
		{/if}
		{@render children()}
	</button>
{/if}
