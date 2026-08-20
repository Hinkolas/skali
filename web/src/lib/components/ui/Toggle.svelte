<script lang="ts">
	// Accessible switch: a button with role="switch"; the label sits beside
	// it and is clickable.
	let {
		checked = $bindable(false),
		label,
		description,
		disabled = false,
		onchange
	}: {
		checked?: boolean;
		label: string;
		description?: string;
		disabled?: boolean;
		onchange?: (checked: boolean) => void;
	} = $props();

	function toggle() {
		if (disabled) return;
		checked = !checked;
		onchange?.(checked);
	}
</script>

<button
	type="button"
	role="switch"
	aria-checked={checked}
	aria-label={label}
	{disabled}
	onclick={toggle}
	class="flex w-full items-center gap-3 text-left {disabled
		? 'cursor-default opacity-60'
		: 'cursor-pointer'}"
>
	<span
		class="relative inline-flex h-5 w-9 flex-none items-center rounded-full transition-colors {checked
			? 'bg-accent'
			: 'bg-white/12'}"
	>
		<span
			class="bg-surface-base inline-block size-4 rounded-full shadow transition-transform {checked
				? 'translate-x-4.5'
				: 'translate-x-0.5'}"
		></span>
	</span>
	<span class="flex flex-col">
		<span class="text-text-secondary text-base font-medium">{label}</span>
		{#if description}
			<span class="text-text-muted text-md leading-relaxed">{description}</span>
		{/if}
	</span>
</button>
