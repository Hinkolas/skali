<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import type { IconProps } from '@lucide/svelte';

	// Shared header for modal content components: one place for the modal type
	// scale (title, description) so the family doesn't drift apart. The icon
	// tile is reserved for guard moments — sudo reauth, destructive confirms —
	// routine forms stay text-only.
	let {
		title,
		description,
		icon: Icon,
		tone = 'accent',
		mono = false,
		children
	}: {
		title: string;
		description?: string;
		icon?: Component<IconProps, object, ''>;
		tone?: 'accent' | 'danger';
		/** Mono title, for modals headed by an identifier (env name, host). */
		mono?: boolean;
		/** Rich description markup; takes the place of `description`. */
		children?: Snippet;
	} = $props();
</script>

<div class="px-5.5 pt-5 pb-2">
	{#if Icon}
		<div
			class="mb-3 flex size-9 items-center justify-center rounded-[11px] border {tone === 'danger'
				? 'border-status-danger/25 bg-status-danger/10 text-status-danger'
				: 'border-accent/25 bg-accent/10 text-accent-light'}"
		>
			<Icon size={20} strokeWidth={1.75} />
		</div>
	{/if}
	<h2 class="text-text-primary text-xl font-semibold tracking-tight {mono ? 'font-mono' : ''}">
		{title}
	</h2>
	{#if description || children}
		<p class="text-text-muted mt-1.5 text-base leading-relaxed">
			{#if children}{@render children()}{:else}{description}{/if}
		</p>
	{/if}
</div>
