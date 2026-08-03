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
		class?: string;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();

	const variantClass: Record<string, string> = {
		primary:
			'bg-linear-135 from-accent-from to-accent-to font-semibold text-surface-base shadow-glow transition-[filter] hover:brightness-108',
		secondary:
			'border border-border-strong bg-white/2 font-medium text-text-secondary transition-colors hover:bg-white/5',
		ghost:
			'font-medium text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary',
		danger: 'bg-status-danger font-semibold text-white transition-[filter] hover:brightness-108'
	};

	const sizeClass: Record<string, string> = {
		sm: 'rounded-[8px] px-2.5 py-1 text-md',
		md: 'rounded-[11px] px-4.5 py-2.25 text-lg'
	};

	const classes = $derived(
		`inline-flex cursor-pointer items-center justify-center gap-2 whitespace-nowrap disabled:cursor-default disabled:opacity-60 ${variantClass[variant]} ${sizeClass[size]} ${className}`
	);
</script>

{#if href}
	<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
	<a {href} class={classes} {onclick}>
		{@render children()}
	</a>
{:else}
	<button {type} disabled={disabled || busy} class={classes} {onclick}>
		{#if busy}
			<LoaderCircle class="size-4 animate-spin" />
		{/if}
		{@render children()}
	</button>
{/if}
