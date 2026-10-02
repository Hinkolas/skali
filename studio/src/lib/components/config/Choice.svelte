<script lang="ts">
	// A read-only segmented display of every value a manifest field accepts,
	// with the declared one lit. Showing the alternatives is the point: the
	// reader learns what else skali.yaml could say without leaving the page.
	let {
		options,
		value,
		label
	}: {
		options: { value: string; label?: string; description?: string }[];
		value: string;
		/** Accessible name for the group. */
		label: string;
	} = $props();
</script>

<div role="group" aria-label={label} class="grid auto-cols-fr grid-flow-col gap-2">
	{#each options as option (option.value)}
		{@const active = option.value === value}
		<div
			aria-current={active ? 'true' : undefined}
			class="flex min-w-0 flex-col gap-0.5 rounded-[11px] border px-3 py-2.5 {active
				? 'border-accent/50 bg-accent/10'
				: 'border-border-default'}"
		>
			<span
				class="font-mono truncate text-md font-medium {active
					? 'text-accent-nav'
					: 'text-text-faint'}"
			>
				{option.label ?? option.value}
			</span>
			{#if option.description}
				<span class="text-xs leading-snug {active ? 'text-text-secondary' : 'text-text-ghost'}">
					{option.description}
				</span>
			{/if}
		</div>
	{/each}
</div>
