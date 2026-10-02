<script lang="ts" generics="T">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import type { Snippet } from 'svelte';

	// The one select style, on the TextInput scale. The native chevron is
	// hidden: once a select carries its own border and background, macOS
	// browsers paint the arrow above the field's middle (#54). A lucide
	// chevron centered over the right padding replaces it.
	let {
		value = $bindable(),
		disabled = false,
		class: className = '',
		children
	}: {
		value?: T;
		disabled?: boolean;
		class?: string;
		children: Snippet;
	} = $props();
</script>

<span class="relative block {className}">
	<select
		bind:value
		{disabled}
		class="bg-surface-input text-text-primary border-border-strong w-full appearance-none rounded-[11px] border py-2.75 pr-9 pl-3.25 text-lg transition-[border-color,box-shadow] duration-150 focus:border-accent/50 focus:ring-3 focus:ring-accent/10 focus:outline-none disabled:opacity-60"
	>
		{@render children()}
	</select>
	<ChevronDown
		size={15}
		class="text-text-tertiary pointer-events-none absolute top-1/2 right-3 -translate-y-1/2"
	/>
</span>
