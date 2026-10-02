<script lang="ts">
	import type { Snippet } from 'svelte';
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import X from '@lucide/svelte/icons/x';

	// A secret field whose chrome tells the value's story: the placeholder
	// says what is stored (or missing), the border tone says whether the
	// field needs attention or holds an unsaved edit, the trailing slot
	// carries facts such as the stored version, and the eye reveals the
	// typed text. `onclear`, passed only while an edit is pending, adds the
	// button that discards it.
	let {
		value = $bindable(''),
		placeholder = '',
		tone = 'default',
		disabled = false,
		label,
		trailing,
		oninput,
		onclear
	}: {
		value?: string;
		placeholder?: string;
		/** warning: needs attention (a missing required value); pending: unsaved edit. */
		tone?: 'default' | 'warning' | 'pending';
		disabled?: boolean;
		/** Accessible name of the input. */
		label: string;
		/** Facts shown inside the field, before the eye. */
		trailing?: Snippet;
		oninput?: (e: Event) => void;
		onclear?: () => void;
	} = $props();

	let revealed = $state(false);

	const toneClass: Record<string, string> = {
		default: 'border-border-strong focus-within:border-accent/50 focus-within:ring-accent/10',
		warning:
			'border-status-warning/50 focus-within:border-status-warning focus-within:ring-status-warning/10',
		pending: 'border-accent/50 focus-within:border-accent/70 focus-within:ring-accent/10'
	};
	const placeholderClass: Record<string, string> = {
		default: 'placeholder:text-text-ghost',
		warning: 'placeholder:text-status-warning/80',
		pending: 'placeholder:text-text-ghost'
	};
</script>

<div
	class="bg-surface-input flex items-center gap-1.5 rounded-[9px] border pr-1.5 pl-3 transition-[border-color,box-shadow] duration-150 focus-within:ring-3 {toneClass[
		tone
	]} {disabled ? 'opacity-60' : ''}"
>
	<input
		type={revealed ? 'text' : 'password'}
		bind:value
		{placeholder}
		{disabled}
		{oninput}
		autocomplete="off"
		aria-label={label}
		class="text-text-primary min-w-0 flex-1 bg-transparent py-1.75 font-mono text-md focus:outline-none {placeholderClass[
			tone
		]}"
	/>
	{#if trailing}
		<span class="text-text-faint flex flex-none items-center gap-1.5 font-mono text-xs">
			{@render trailing()}
		</span>
	{/if}
	{#if onclear}
		<button
			type="button"
			onclick={onclear}
			aria-label="Discard the edit to {label}"
			title="Discard this edit"
			class="text-text-faint hover:text-text-primary flex size-6 flex-none cursor-pointer items-center justify-center rounded-md transition-colors hover:bg-white/5"
		>
			<X size={13} />
		</button>
	{/if}
	<button
		type="button"
		onclick={() => (revealed = !revealed)}
		{disabled}
		aria-pressed={revealed}
		aria-label={revealed ? 'Hide the value' : 'Show the value'}
		title={revealed ? 'Hide' : 'Show'}
		class="text-text-faint hover:text-text-primary flex size-6 flex-none cursor-pointer items-center justify-center rounded-md transition-colors hover:bg-white/5 disabled:cursor-default"
	>
		{#if revealed}
			<EyeOff size={13} />
		{:else}
			<Eye size={13} />
		{/if}
	</button>
</div>
