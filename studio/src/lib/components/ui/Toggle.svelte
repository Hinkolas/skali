<script lang="ts">
	// Accessible switch: a button with role="switch"; the label sits beside
	// it and is clickable.
	import Switch from '$lib/components/ui/Switch.svelte';

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
	<Switch {checked} />
	<span class="flex flex-col">
		<span class="text-text-secondary text-base font-medium">{label}</span>
		{#if description}
			<span class="text-text-muted text-md leading-relaxed">{description}</span>
		{/if}
	</span>
</button>
