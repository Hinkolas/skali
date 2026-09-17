<script lang="ts">
	import type { MetricsWindow } from '$lib/types/metrics';

	// The metrics window switch: the three fixed windows the API serves.
	let { value, onchange }: { value: MetricsWindow; onchange: (window: MetricsWindow) => void } =
		$props();

	const WINDOWS: { value: MetricsWindow; label: string }[] = [
		{ value: '1h', label: '1 hour' },
		{ value: '24h', label: '24 hours' },
		{ value: '7d', label: '7 days' }
	];
</script>

<div class="flex flex-none items-center gap-1" role="group" aria-label="Window">
	{#each WINDOWS as option (option.value)}
		<button
			type="button"
			aria-pressed={value === option.value}
			onclick={() => onchange(option.value)}
			class="cursor-pointer rounded-full px-3 py-1 text-sm whitespace-nowrap transition-colors {value ===
			option.value
				? 'bg-white/8 text-text-primary'
				: 'text-text-tertiary hover:text-text-secondary'}"
		>
			{option.label}
		</button>
	{/each}
</div>
