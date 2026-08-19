<script lang="ts">
	// A compact select built on Menu: the trigger shows the current choice,
	// the panel lists the options with a hint each. Disabled, it renders the
	// value as plain text with the explanation as its title.
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';

	export interface RoleOption {
		value: string;
		label?: string;
		hint?: string;
		danger?: boolean;
	}

	let {
		value,
		options,
		label,
		disabled = false,
		title,
		busy = false,
		placeholder = 'choose',
		mono = true,
		onchange
	}: {
		value: string | null;
		options: RoleOption[];
		/** aria-label of the menu panel. */
		label: string;
		disabled?: boolean;
		title?: string;
		busy?: boolean;
		placeholder?: string;
		mono?: boolean;
		onchange: (value: string) => void;
	} = $props();

	const current = $derived(options.find((o) => o.value === value));
	const text = $derived(current?.label ?? current?.value ?? value ?? placeholder);
	const textClass = $derived(mono ? 'font-mono text-md' : 'text-base');
</script>

{#if disabled}
	<span class="text-text-secondary inline-flex items-center px-2 py-1 {textClass}" {title}>
		{text}
	</span>
{:else}
	<Menu
		{label}
		triggerClass="inline-flex cursor-pointer items-center gap-1 rounded-lg border border-border-strong bg-white/2 px-2 py-1 text-text-secondary transition-colors hover:bg-white/5 disabled:opacity-60 {busy
			? 'opacity-60'
			: ''}"
		panelClass="min-w-60"
	>
		{#snippet trigger({ open })}
			<span class={textClass}>{text}</span>
			<ChevronDown
				size={13}
				class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
			/>
		{/snippet}
		{#each options as option (option.value)}
			<MenuItem
				selected={option.value === value}
				danger={option.danger}
				onselect={() => option.value !== value && onchange(option.value)}
			>
				<span class="flex flex-col">
					<span class={textClass}>{option.label ?? option.value}</span>
					{#if option.hint}
						<span class="text-text-ghost text-xs font-normal">{option.hint}</span>
					{/if}
				</span>
			</MenuItem>
		{/each}
	</Menu>
{/if}
